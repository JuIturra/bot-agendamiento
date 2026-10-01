package panel

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeDatos struct{}

var t0 = time.Date(2026, 10, 1, 15, 0, 0, 0, time.UTC)

const malicioso = `<script>alert("x")</script>`

func (fakeDatos) Resumen(context.Context, string, time.Time, time.Time, string) (Resumen, error) {
	return Resumen{Conversaciones: 4, ConversacionesConReserva: 1, Reservas: 2, LLMHoy: 7,
		CalendarActivo: true, CalendarConError: 1, CalendarUltimoError: "freebusy: notFound"}, nil
}
func (fakeDatos) Conversaciones(context.Context, string, int) ([]ConversacionFila, error) {
	return []ConversacionFila{{ID: "c1", Cliente: malicioso, Canal: "telegram", Ultimo: t0, Agendo: true}}, nil
}
func (fakeDatos) Conversacion(_ context.Context, _, id string) (Conversacion, error) {
	if id != "c1" {
		return Conversacion{}, ErrNoEncontrada
	}
	return Conversacion{
		ConversacionFila: ConversacionFila{ID: "c1", Cliente: "Ana", MensajesCliente: 1},
		Mensajes: []Mensaje{
			{Rol: "user", At: t0, Texto: malicioso},
			{Rol: "tool", At: t0, Tool: "agendar", Input: `{"servicio":"Corte"}`, Result: "OK. Reserva confirmada."},
			{Rol: "assistant", At: t0, Texto: "Listo, quedaste agendada.", TokensIn: 1234},
		},
	}, nil
}
func (fakeDatos) Reservas(context.Context, string, time.Time, int) ([]ReservaFila, error) {
	return []ReservaFila{{Servicio: "Corte", Cliente: "Ana", Estado: "confirmada", Inicio: t0, EnCalendar: true}}, nil
}

func nuevo(t *testing.T, password string) http.Handler {
	t.Helper()
	p, err := New(Config{
		Datos: fakeDatos{}, Negocio: "Barbería El Filo", Loc: time.UTC, LimiteRPD: 20,
		UltimoPoll: func() time.Time { return t0 }, Password: password,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return t0.Add(time.Minute) },
	})
	if err != nil {
		t.Fatal(err)
	}
	return p.Handler()
}

func get(h http.Handler, path, user, pass string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAutenticacion(t *testing.T) {
	h := nuevo(t, "clave-larga")
	if rec := get(h, "/healthz", "", ""); rec.Code != 200 {
		t.Errorf("/healthz sin auth = %d", rec.Code)
	}
	for _, path := range []string{"/", "/conversaciones", "/conversaciones/c1", "/reservas"} {
		if rec := get(h, path, "", ""); rec.Code != 401 || rec.Header().Get("WWW-Authenticate") == "" {
			t.Errorf("%s sin auth = %d", path, rec.Code)
		}
		if rec := get(h, path, "admin", "otra"); rec.Code != 401 {
			t.Errorf("%s con clave incorrecta = %d", path, rec.Code)
		}
		if rec := get(h, path, "intruso", "clave-larga"); rec.Code != 401 {
			t.Errorf("%s con usuario incorrecto = %d", path, rec.Code)
		}
		rec := get(h, path, "admin", "clave-larga")
		if rec.Code != 200 {
			t.Errorf("%s autenticado = %d: %s", path, rec.Code, rec.Body)
		}
		for _, hdr := range []string{"Cache-Control", "X-Frame-Options", "Content-Security-Policy"} {
			if rec.Header().Get(hdr) == "" {
				t.Errorf("%s: falta el header %s", path, hdr)
			}
		}
	}
}

func TestSinPasswordNoSePublica(t *testing.T) {
	h := nuevo(t, "")
	if rec := get(h, "/healthz", "", ""); rec.Code != 200 {
		t.Errorf("/healthz = %d", rec.Code)
	}
	// Ni siquiera con un usuario y contraseña vacíos.
	if rec := get(h, "/", "admin", ""); rec.Code != 404 {
		t.Errorf("/ sin PANEL_PASSWORD = %d", rec.Code)
	}
}

func TestContenidoYEscape(t *testing.T) {
	h := nuevo(t, "k")
	resumen := get(h, "/", "admin", "k").Body.String()
	for _, want := range []string{"25%", "7", "/ 20", "freebusy: notFound", "1 con error", "conectado"} {
		if !strings.Contains(resumen, want) {
			t.Errorf("resumen: falta %q", want)
		}
	}
	// El nombre y los mensajes del cliente vienen de Telegram: deben escaparse.
	for _, path := range []string{"/conversaciones", "/conversaciones/c1"} {
		body := get(h, path, "admin", "k").Body.String()
		if strings.Contains(body, "<script>") {
			t.Errorf("%s: HTML del cliente sin escapar", path)
		}
	}
	det := get(h, "/conversaciones/c1", "admin", "k").Body.String()
	// Regresión: Conversacion embebía una fila con un campo del mismo nombre que la
	// lista de mensajes, y el encabezado imprimía la lista entera.
	if strings.Contains(det, "{user") || strings.Contains(det, "[{") {
		t.Error("el encabezado imprime la estructura de los mensajes")
	}
	for _, want := range []string{"1 mensajes del cliente", "agendar", "OK. Reserva confirmada.", "Listo, quedaste agendada.", "1.234 tokens"} {
		if !strings.Contains(det, want) {
			t.Errorf("detalle: falta %q", want)
		}
	}
	if rec := get(h, "/conversaciones/otra", "admin", "k"); rec.Code != 404 {
		t.Errorf("conversación inexistente = %d", rec.Code)
	}
}
