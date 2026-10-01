package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"agendamiento/internal/booking"
	"agendamiento/internal/llm"
	"agendamiento/internal/store"
	"agendamiento/internal/tenant"
	"agendamiento/internal/tools"
)

// guion es un proveedor falso que devuelve respuestas predefinidas y registra lo
// que recibe. Permite probar el loop completo sin llamar a ninguna API.
type guion struct {
	respuestas []llm.Response
	recibidos  []llm.Request
}

func (g *guion) Name() string { return "guion/test" }

func (g *guion) Chat(_ context.Context, req llm.Request) (llm.Response, error) {
	g.recibidos = append(g.recibidos, req)
	if len(g.recibidos) > len(g.respuestas) {
		return llm.Response{}, fmt.Errorf("llamada %d no prevista", len(g.recibidos))
	}
	return g.respuestas[len(g.recibidos)-1], nil
}

func openTestEngine(t *testing.T, g *guion) (*Engine, *booking.Service, time.Time) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL no definida")
	}
	ctx := context.Background()
	st, err := store.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	cfg, err := tenant.Parse([]byte(fmt.Sprintf(`
slug: engine-%d
nombre: Test
horario: {martes: ["10:00-12:00"]}
servicios: [{nombre: Corte, duracion_minutos: 30, precio: 10000}]
`, time.Now().UnixNano())))
	if err != nil {
		t.Fatal(err)
	}
	cfg.ModeloLLM = "guion/test"
	tenantID, err := st.UpsertTenant(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 5, 9, 0, 0, 0, cfg.Location())
	svc := &booking.Service{Cfg: cfg, TenantID: tenantID, Store: st, Now: func() time.Time { return now }}
	e := &Engine{
		LLM: g, Store: st, Tenant: cfg, TenantID: tenantID,
		Tools: &tools.Toolset{Booking: svc},
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	e.Init()
	return e, svc, now
}

// Si el modelo falla después de que agendar ya reservó (p. ej. un 429), el cliente
// debe recibir la confirmación, no un "tuve un problema". Pasó con Gemini en la
// primera prueba real.
func TestFalloDespuesDeAgendarConfirmaIgual(t *testing.T) {
	g := &guion{respuestas: []llm.Response{
		llamada("c1", "agendar", `{"servicio":"Corte","inicio":"2026-10-06 10:30","nombre_cliente":"Ana"}`),
		// no hay segunda respuesta: la llamada falla
	}}
	e, _, now := openTestEngine(t, g)
	resp, err := e.Handle(context.Background(), Inbound{
		Canal: "test", ExternalUserID: "u2", Texto: "agéndame el martes 10:30, soy Ana", Enviado: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp, "Tu hora quedó reservada: Corte, el martes 06-10-2026 a las 10:30, a nombre de Ana.") {
		t.Errorf("respuesta = %q", resp)
	}
}

func llamada(id, name, input string) llm.Response {
	return llm.Response{
		Stop:    llm.StopToolUse,
		Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: id, Name: name, Input: json.RawMessage(input)}}},
		Usage:   llm.Usage{In: 100, Out: 10},
	}
}

func TestLoopAgendaDeInicioAFin(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL no definida")
	}
	ctx := context.Background()
	st, err := store.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	cfg, err := tenant.Parse([]byte(fmt.Sprintf(`
slug: engine-%d
nombre: Test
horario: {martes: ["10:00-12:00"]}
servicios: [{nombre: Corte, duracion_minutos: 30, precio: 10000}]
`, time.Now().UnixNano())))
	if err != nil {
		t.Fatal(err)
	}
	cfg.ModeloLLM = "guion/test"
	tenantID, err := st.UpsertTenant(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 5, 9, 0, 0, 0, cfg.Location())
	svc := &booking.Service{Cfg: cfg, TenantID: tenantID, Store: st, Now: func() time.Time { return now }}

	g := &guion{respuestas: []llm.Response{
		llamada("c1", "consultar_disponibilidad", `{"servicio":"Corte","fecha_desde":"2026-10-06","fecha_hasta":"2026-10-06","franja":"cualquiera"}`),
		llamada("c2", "agendar", `{"servicio":"Corte","inicio":"2026-10-06 10:30","nombre_cliente":"Ana"}`),
		{Stop: llm.StopEnd, Message: llm.Message{Role: llm.RoleAssistant, Text: "Listo, quedaste el martes a las 10:30."}, Usage: llm.Usage{In: 100, Out: 10}},
	}}
	e := &Engine{
		LLM: g, Store: st, Tenant: cfg, TenantID: tenantID,
		Tools: &tools.Toolset{Booking: svc},
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	e.Init()

	resp, err := e.Handle(ctx, Inbound{
		Canal: "test", ExternalUserID: "u1", Nombre: "Ana", Texto: "quiero hora el martes", Enviado: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp != "Listo, quedaste el martes a las 10:30." {
		t.Errorf("respuesta = %q", resp)
	}

	// Segunda llamada: debe llevar el turno con la tool call y su resultado.
	r2 := g.recibidos[1].Messages
	ultimo := r2[len(r2)-1]
	if len(ultimo.ToolResults) != 1 || ultimo.ToolResults[0].CallID != "c1" ||
		!strings.Contains(ultimo.ToolResults[0].Content, "10:00, 10:30, 11:00, 11:30") {
		t.Errorf("resultado de consultar_disponibilidad = %+v", ultimo.ToolResults)
	}
	// Tercera: el resultado de agendar debe confirmar la reserva.
	r3 := g.recibidos[2].Messages
	if res := r3[len(r3)-1].ToolResults[0]; res.IsError || !strings.HasPrefix(res.Content, "OK.") {
		t.Errorf("resultado de agendar = %+v", res)
	}
	// El primer mensaje del usuario lleva fecha y nombre de perfil.
	if u := g.recibidos[0].Messages[0].Text; !strings.Contains(u, "[lunes 05-10-2026 a las 09:00]") || !strings.Contains(u, "Ana") {
		t.Errorf("mensaje de usuario = %q", u)
	}

	// La reserva quedó en la base y el horario ya no se ofrece.
	libres, err := svc.Disponibilidad(ctx, "Corte", now.AddDate(0, 0, 1), now.AddDate(0, 0, 1), booking.FranjaCualquiera)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range libres {
		if s.Format("15:04") == "10:30" {
			t.Error("10:30 sigue libre después de agendar")
		}
	}

	// Se registró el consumo sumado de las 3 llamadas.
	var tin, tout int64
	if err := st.Pool().QueryRow(ctx, `
		SELECT tokens_in, tokens_out FROM messages m JOIN conversations c ON c.id = m.conversation_id
		WHERE c.tenant_id = $1 AND m.rol = 'assistant'`, tenantID).Scan(&tin, &tout); err != nil {
		t.Fatal(err)
	}
	if tin != 300 || tout != 30 {
		t.Errorf("tokens = %d/%d, want 300/30", tin, tout)
	}
}
