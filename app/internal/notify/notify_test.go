package notify

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

var silencio = slog.New(slog.NewTextHandler(io.Discard, nil))

func pendiente(confirmada, avisada bool) Pendiente {
	return Pendiente{
		AppointmentID: "a1", ChatID: 42, TimeZone: "America/Santiago",
		Servicio: "Corte de pelo", NombreCliente: "Ana", Canal: "telegram",
		Inicio:     time.Date(2026, 10, 2, 13, 0, 0, 0, time.UTC), // 10:00 en Chile
		Confirmada: confirmada, AvisadaCreacion: avisada,
	}
}

func TestMensaje(t *testing.T) {
	texto, cre, can := Mensaje(pendiente(true, false))
	if !strings.Contains(texto, "Nueva reserva") || !strings.Contains(texto, "viernes 02-10-2026 a las 10:00") ||
		!strings.Contains(texto, "Ana (Telegram)") || !cre || can {
		t.Errorf("reserva: %q cre=%v can=%v", texto, cre, can)
	}
	texto, cre, can = Mensaje(pendiente(false, true))
	if !strings.Contains(texto, "Reserva cancelada") || !strings.Contains(texto, "quedó libre") || !cre || !can {
		t.Errorf("cancelación: %q cre=%v can=%v", texto, cre, can)
	}
	// Cancelada antes de que el dueño supiera de ella: no se avisa nada.
	texto, cre, can = Mensaje(pendiente(false, false))
	if texto != "" || !cre || !can {
		t.Errorf("cancelación sin aviso previo: %q cre=%v can=%v", texto, cre, can)
	}
}

type fakeStore struct {
	pendientes []Pendiente
	marcados   map[string][2]bool
	errores    map[string]string
	registro   int64
}

func (f *fakeStore) AvisosPendientes(context.Context, int) ([]Pendiente, error) {
	return f.pendientes, nil
}
func (f *fakeStore) MarcarAviso(_ context.Context, id string, cre, can bool) error {
	f.marcados[id] = [2]bool{cre, can}
	return nil
}
func (f *fakeStore) AvisoError(_ context.Context, id, msg string) error {
	f.errores[id] = msg
	return nil
}
func (f *fakeStore) RegistrarDueno(_ context.Context, _ string, chatID int64) error {
	f.registro = chatID
	return nil
}

type fakeSender struct {
	enviados []string
	fallar   bool
}

func (f *fakeSender) Send(_ context.Context, _ int64, text string) error {
	if f.fallar {
		return errors.New("telegram caído")
	}
	f.enviados = append(f.enviados, text)
	return nil
}

func TestEnviarPendientes(t *testing.T) {
	cancelada := pendiente(false, false)
	cancelada.AppointmentID = "a2"
	st := &fakeStore{pendientes: []Pendiente{pendiente(true, false), cancelada},
		marcados: map[string][2]bool{}, errores: map[string]string{}}
	snd := &fakeSender{}
	n := &Notifier{Store: st, Sender: snd, Log: silencio}

	enviados, err := n.EnviarPendientes(context.Background())
	if err != nil || enviados != 1 || len(snd.enviados) != 1 {
		t.Fatalf("enviados = %d, err = %v, mensajes = %v", enviados, err, snd.enviados)
	}
	// La cancelación sin aviso previo queda resuelta sin mensaje.
	if st.marcados["a1"] != [2]bool{true, false} || st.marcados["a2"] != [2]bool{true, true} {
		t.Errorf("marcados = %v", st.marcados)
	}

	// Telegram caído: se registra el error y no se marca como enviado.
	st.marcados = map[string][2]bool{}
	st.pendientes = []Pendiente{pendiente(true, false)}
	snd.fallar = true
	if enviados, err := n.EnviarPendientes(context.Background()); err != nil || enviados != 0 {
		t.Fatalf("enviados = %d, err = %v", enviados, err)
	}
	if _, ok := st.marcados["a1"]; ok || st.errores["a1"] == "" {
		t.Errorf("con error no debe marcarse: marcados = %v, errores = %v", st.marcados, st.errores)
	}
}

func TestRegistro(t *testing.T) {
	st := &fakeStore{}
	r := &Registro{Store: st, TenantID: "t1", Negocio: "Barbería El Filo", Codigo: "secreto-123", Log: silencio}
	ctx := context.Background()

	if _, ok, _ := r.Handle(ctx, 7, "hola, ¿tienen hora?"); ok {
		t.Error("un mensaje normal no es el comando")
	}
	if resp, ok, _ := r.Handle(ctx, 7, "/dueno otro"); !ok || resp != "Código inválido." || st.registro != 0 {
		t.Errorf("código incorrecto: %q, %v, registro = %d", resp, ok, st.registro)
	}
	if resp, ok, err := r.Handle(ctx, 7, "/DUENO  secreto-123 "); !ok || err != nil || !strings.Contains(resp, "Barbería El Filo") || st.registro != 7 {
		t.Errorf("código correcto: %q, %v, %v, registro = %d", resp, ok, err, st.registro)
	}
	// Sin código configurado, el registro está desactivado: ni un código vacío pasa.
	r.Codigo, st.registro = "", 0
	if resp, ok, _ := r.Handle(ctx, 7, "/dueno"); !ok || resp != "Código inválido." || st.registro != 0 {
		t.Errorf("registro desactivado: %q, %v, registro = %d", resp, ok, st.registro)
	}
}
