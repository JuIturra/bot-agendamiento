package anthropic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agendamiento/internal/llm"
)

// Respuesta con thinking + texto + tool_use, como la devuelve la API.
const respToolUse = `{
  "id": "msg_1", "type": "message", "role": "assistant", "model": "claude-opus-5",
  "content": [
    {"type": "thinking", "thinking": "", "signature": "firma-123"},
    {"type": "text", "text": "Reviso la agenda."},
    {"type": "tool_use", "id": "toolu_1", "name": "consultar_disponibilidad", "input": {"servicio": "Corte"}}
  ],
  "stop_reason": "tool_use", "stop_sequence": null,
  "usage": {"input_tokens": 100, "output_tokens": 20, "cache_creation_input_tokens": 10, "cache_read_input_tokens": 50}
}`

const respFin = `{
  "id": "msg_2", "type": "message", "role": "assistant", "model": "claude-opus-5",
  "content": [{"type": "text", "text": "Tengo 10:00 y 11:00."}],
  "stop_reason": "end_turn", "stop_sequence": null,
  "usage": {"input_tokens": 10, "output_tokens": 5}
}`

type captura struct {
	bodies  []map[string]any
	headers []http.Header
}

func servidor(t *testing.T, respuestas ...string) (*httptest.Server, *captura) {
	t.Helper()
	c := &captura{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("body inválido: %v", err)
		}
		c.bodies = append(c.bodies, body)
		c.headers = append(c.headers, r.Header.Clone())
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, respuestas[len(c.bodies)-1])
	}))
	t.Cleanup(srv.Close)
	return srv, c
}

var toolDemo = llm.Tool{
	Name: "consultar_disponibilidad", Description: "Busca horarios", Strict: true,
	Schema: map[string]any{
		"type":                 "object",
		"properties":           map[string]any{"servicio": map[string]any{"type": "string"}},
		"required":             []string{"servicio"},
		"additionalProperties": false,
	},
}

func TestLoopConThinking(t *testing.T) {
	srv, cap := servidor(t, respToolUse, respFin)
	p := New(Config{APIKey: "test", Model: "claude-opus-5", BaseURL: srv.URL})
	ctx := context.Background()
	req := llm.Request{
		System: "sistema", Tools: []llm.Tool{toolDemo}, MaxTokens: 100,
		Messages: []llm.Message{{Role: llm.RoleUser, Text: "hola"}},
	}

	r1, err := p.Chat(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if r1.Stop != llm.StopToolUse || len(r1.Message.ToolCalls) != 1 {
		t.Fatalf("se esperaba una tool call: %+v", r1)
	}
	if c := r1.Message.ToolCalls[0]; c.ID != "toolu_1" || string(c.Input) != `{"servicio": "Corte"}` {
		t.Errorf("tool call = %+v", c)
	}
	if r1.Usage != (llm.Usage{In: 160, Out: 20, CacheRead: 50}) {
		t.Errorf("usage = %+v", r1.Usage)
	}

	// Segunda vuelta: el turno del asistente debe reenviarse con su thinking.
	req.Messages = append(req.Messages, r1.Message, llm.Message{
		Role: llm.RoleUser, ToolResults: []llm.ToolResult{{CallID: "toolu_1", Content: "10:00, 11:00"}},
	})
	r2, err := p.Chat(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if r2.Stop != llm.StopEnd || r2.Message.Text != "Tengo 10:00 y 11:00." {
		t.Errorf("r2 = %+v", r2)
	}

	b := cap.bodies[0]
	tool := b["tools"].([]any)[0].(map[string]any)
	schema := tool["input_schema"].(map[string]any)
	if tool["strict"] != true || schema["additionalProperties"] != false {
		t.Errorf("tool no estricta: %v", tool)
	}
	if b["system"].([]any)[0].(map[string]any)["cache_control"] == nil {
		t.Error("falta cache_control en system")
	}
	if b["thinking"].(map[string]any)["type"] != "adaptive" || b["output_config"].(map[string]any)["effort"] != "low" {
		t.Errorf("thinking/effort = %v / %v", b["thinking"], b["output_config"])
	}
	if b["fallbacks"] != "default" || !strings.Contains(cap.headers[0].Get("anthropic-beta"), "server-side-fallback-2026-07-01") {
		t.Error("falta el fallback de Opus 5")
	}

	msgs := cap.bodies[1]["messages"].([]any)
	asist, _ := json.Marshal(msgs[1])
	if !strings.Contains(string(asist), "firma-123") {
		t.Errorf("el thinking no se reenvió: %s", asist)
	}
	res, _ := json.Marshal(msgs[2])
	if !strings.Contains(string(res), `"tool_use_id":"toolu_1"`) {
		t.Errorf("tool_result mal armado: %s", res)
	}
}

func TestHaikuSinThinking(t *testing.T) {
	srv, cap := servidor(t, respFin)
	p := New(Config{APIKey: "test", Model: "claude-haiku-4-5", BaseURL: srv.URL})
	if _, err := p.Chat(context.Background(), llm.Request{
		System: "s", MaxTokens: 10, Messages: []llm.Message{{Role: llm.RoleUser, Text: "hola"}},
	}); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"thinking", "output_config", "fallbacks"} {
		if _, ok := cap.bodies[0][k]; ok {
			t.Errorf("Haiku no debería recibir %s", k)
		}
	}
}
