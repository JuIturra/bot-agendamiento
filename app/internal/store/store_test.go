package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"agendamiento/internal/booking"
	"agendamiento/internal/llm/limit"
	"agendamiento/internal/tenant"
)

// Estos tests necesitan Postgres: `make test-integration` levanta el de
// docker-compose y define TEST_DATABASE_URL.
func openTest(t *testing.T) *Store {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL no definida")
	}
	if os.Getenv("DATABASE_URL") != "" && url == os.Getenv("DATABASE_URL") {
		t.Fatal("TEST_DATABASE_URL no puede ser la base de desarrollo: el bot procesaría los datos de los tests")
	}
	st, err := Open(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	return st
}

func setup(t *testing.T, st *Store) (*booking.Service, []string) {
	t.Helper()
	ctx := context.Background()
	slug := fmt.Sprintf("test-%d", time.Now().UnixNano())
	cfg, err := tenant.Parse([]byte(`
slug: ` + slug + `
nombre: Test
horario: {martes: ["10:00-14:00"]}
dias_maximos_adelanto: 60
servicios: [{nombre: Corte, duracion_minutos: 30}]
`))
	if err != nil {
		t.Fatal(err)
	}
	cfg.ModeloLLM = "test"
	tenantID, err := st.UpsertTenant(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	var clientes []string
	for i := 0; i < 10; i++ {
		id, err := st.UpsertCustomer(ctx, tenantID, "test", fmt.Sprint(i), "")
		if err != nil {
			t.Fatal(err)
		}
		clientes = append(clientes, id)
	}
	// "Ahora" fijo en un lunes: el martes siguiente queda disponible.
	now := time.Date(2026, 10, 5, 9, 0, 0, 0, cfg.Location())
	return &booking.Service{Cfg: cfg, TenantID: tenantID, Store: st, Now: func() time.Time { return now }}, clientes
}

// Diez clientes piden la misma hora al mismo tiempo: la validación en Go pasa para
// todos (nadie ve la reserva de los otros), y es la restricción EXCLUDE la que deja
// pasar solo una.
func TestReservasConcurrentesMismoHorario(t *testing.T) {
	st := openTest(t)
	svc, clientes := setup(t, st)
	inicio := time.Date(2026, 10, 6, 11, 0, 0, 0, svc.Cfg.Location())

	var wg sync.WaitGroup
	errs := make([]error, len(clientes))
	start := make(chan struct{})
	for i, c := range clientes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = svc.Agendar(context.Background(), c, "Corte", inicio, "Cliente")
		}()
	}
	close(start)
	wg.Wait()

	ok, ocupado := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, booking.ErrOcupado):
			ocupado++
		default:
			t.Errorf("error inesperado: %v", err)
		}
	}
	if ok != 1 || ocupado != len(clientes)-1 {
		t.Fatalf("ok = %d, ocupado = %d; se esperaba 1 y %d", ok, ocupado, len(clientes)-1)
	}
}

func TestCancelarSoloPropia(t *testing.T) {
	st := openTest(t)
	svc, clientes := setup(t, st)
	ctx := context.Background()
	inicio := time.Date(2026, 10, 6, 12, 0, 0, 0, svc.Cfg.Location())

	r, err := svc.Agendar(ctx, clientes[0], "Corte", inicio, "Ana")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Cancelar(ctx, clientes[1], r.ID); !errors.Is(err, booking.ErrNoEncontrada) {
		t.Fatalf("otro cliente pudo cancelar: err = %v", err)
	}
	if _, err := svc.Cancelar(ctx, clientes[0], "no-es-un-uuid"); !errors.Is(err, booking.ErrNoEncontrada) {
		t.Fatalf("id inválido: err = %v", err)
	}
	if _, err := svc.Cancelar(ctx, clientes[0], r.ID); err != nil {
		t.Fatal(err)
	}
	// Cancelada, el horario vuelve a estar libre.
	if _, err := svc.Agendar(ctx, clientes[1], "Corte", inicio, "Beto"); err != nil {
		t.Fatalf("el horario debería haberse liberado: %v", err)
	}
	rs, err := svc.MisReservas(ctx, clientes[0])
	if err != nil || len(rs) != 0 {
		t.Fatalf("MisReservas del cliente 0 = %v, %v; se esperaba vacío", rs, err)
	}
}

// Dos procesos (dos wrappers sobre el mismo Store) comparten el cupo diario.
func TestCuotaLLMCompartidaEntreProcesos(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	clave := fmt.Sprintf("test/%d", time.Now().UnixNano())
	t.Cleanup(func() { st.Pool().Exec(ctx, `DELETE FROM llm_requests WHERE clave = $1`, clave) })
	dia := time.Now().Add(-time.Hour)
	ok := 0
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, reservado, err := st.Reserve(ctx, clave, dia, time.Now(), func(u limit.Uso) bool { return u.DiaActual < 3 })
			if err != nil {
				t.Error(err)
			}
			if reservado {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if ok != 3 {
		t.Fatalf("se reservaron %d requests con un cupo de 3", ok)
	}
}

// Ciclo de sincronización con Calendar: reservar → pendiente de crear → sincronizada →
// cancelar → pendiente de borrar → error → no se reintenta hasta el backoff.
func TestCicloSyncCalendar(t *testing.T) {
	st := openTest(t)
	svc, clientes := setup(t, st)
	ctx := context.Background()
	calID := "test-" + svc.TenantID + "@group.calendar.google.com"
	if _, err := st.Pool().Exec(ctx, `UPDATE tenants SET calendar_id = $1 WHERE id = $2`, calID, svc.TenantID); err != nil {
		t.Fatal(err)
	}
	// Un calendario falso no debe quedar configurado aunque el test falle a la mitad.
	t.Cleanup(func() {
		st.Pool().Exec(context.Background(), `UPDATE tenants SET calendar_id = NULL WHERE id = $1`, svc.TenantID)
	})
	// La reserva tiene que ser futura de verdad: PendientesCalendar usa now().
	now := time.Now()
	dias := (int(time.Tuesday) - int(now.Weekday()) + 7) % 7
	if dias == 0 {
		dias = 7
	}
	martes := now.AddDate(0, 0, dias)
	inicio := time.Date(martes.Year(), martes.Month(), martes.Day(), 11, 0, 0, 0, svc.Cfg.Location())
	svc.Now = time.Now
	svc.Cfg.DiasMaximosAdelanto = 60

	r, err := svc.Agendar(ctx, clientes[0], "Corte", inicio, "Ana")
	if err != nil {
		t.Fatal(err)
	}
	pendiente := func() (bool, bool) {
		ps, err := st.PendientesCalendar(ctx, 1000)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range ps {
			if p.AppointmentID == r.ID {
				if p.CalendarID != calID || p.NombreCliente != "Ana" || p.Canal != "test" {
					t.Errorf("pendiente = %+v", p)
				}
				return true, p.Confirmada
			}
		}
		return false, false
	}

	if ok, crear := pendiente(); !ok || !crear {
		t.Fatal("una reserva nueva debería quedar pendiente de crear")
	}
	if err := st.CalendarSincronizado(ctx, r.ID, "ev1"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := pendiente(); ok {
		t.Fatal("una reserva sincronizada no debería estar pendiente")
	}
	if _, err := svc.Cancelar(ctx, clientes[0], r.ID); err != nil {
		t.Fatal(err)
	}
	if ok, crear := pendiente(); !ok || crear {
		t.Fatal("una reserva cancelada con evento debería quedar pendiente de borrar")
	}
	if err := st.CalendarError(ctx, r.ID, "google caído"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := pendiente(); ok {
		t.Fatal("después de un error no se reintenta hasta que pase el backoff")
	}
	var intentos int
	var espera time.Duration
	st.Pool().QueryRow(ctx, `SELECT gcal_intentos, extract(epoch FROM gcal_proximo_intento - now())::int * interval '1 second'
		FROM appointments WHERE id = $1`, r.ID).Scan(&intentos, &espera)
	if intentos != 1 || espera < 50*time.Second || espera > 70*time.Second {
		t.Errorf("intentos = %d, espera = %v; se esperaba 1 intento y ~1 minuto", intentos, espera)
	}
}

// Avisos al dueño: lo anterior al registro no se avisa; reserva → aviso pendiente →
// marcado → cancelación → aviso de cancelación con "ya avisada" → error con backoff.
func TestCicloAvisosDueno(t *testing.T) {
	st := openTest(t)
	svc, clientes := setup(t, st)
	ctx := context.Background()
	svc.Now = time.Now
	svc.Cfg.DiasMaximosAdelanto = 60
	now := time.Now()
	dias := (int(time.Tuesday) - int(now.Weekday()) + 7) % 7
	if dias == 0 {
		dias = 7
	}
	martes := now.AddDate(0, 0, dias)
	a := func(h int) time.Time {
		return time.Date(martes.Year(), martes.Month(), martes.Day(), h, 0, 0, 0, svc.Cfg.Location())
	}

	antes, err := svc.Agendar(ctx, clientes[0], "Corte", a(10), "Antes")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	if err := st.RegistrarDueno(ctx, svc.TenantID, 555); err != nil {
		t.Fatal(err)
	}
	r, err := svc.Agendar(ctx, clientes[1], "Corte", a(11), "Ana")
	if err != nil {
		t.Fatal(err)
	}
	buscar := func(id string) (notifyEstado string) {
		ps, err := st.AvisosPendientes(ctx, 1000)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range ps {
			if p.AppointmentID == id {
				if p.ChatID != 555 || p.NombreCliente == "" {
					t.Errorf("pendiente = %+v", p)
				}
				return fmt.Sprintf("confirmada=%v avisada=%v", p.Confirmada, p.AvisadaCreacion)
			}
		}
		return "no"
	}

	if got := buscar(antes.ID); got != "no" {
		t.Errorf("una reserva anterior al registro no se avisa: %s", got)
	}
	if got := buscar(r.ID); got != "confirmada=true avisada=false" {
		t.Fatalf("reserva nueva: %s", got)
	}
	if err := st.MarcarAviso(ctx, r.ID, true, false); err != nil {
		t.Fatal(err)
	}
	if got := buscar(r.ID); got != "no" {
		t.Fatalf("ya avisada: %s", got)
	}
	if _, err := svc.Cancelar(ctx, clientes[1], r.ID); err != nil {
		t.Fatal(err)
	}
	if got := buscar(r.ID); got != "confirmada=false avisada=true" {
		t.Fatalf("cancelación: %s", got)
	}
	if err := st.AvisoError(ctx, r.ID, "telegram caído"); err != nil {
		t.Fatal(err)
	}
	if got := buscar(r.ID); got != "no" {
		t.Fatalf("después de un error se espera el backoff: %s", got)
	}
}
