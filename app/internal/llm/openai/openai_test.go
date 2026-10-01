package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agendamiento/internal/llm"
)

func TestChat(t *testing.T) {
	var body map[string]any
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		auth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &body)
		// Un servidor compatible que marca tool_calls con finish_reason "stop".
		io.WriteString(w, `{
		  "choices": [{"finish_reason": "stop", "message": {"role": "assistant",
		    "content": "<think>razono</think>Reviso.",
		    "tool_calls": [{"id": "call_1", "type": "function",
		      "function": {"name": "agendar", "arguments": "{\"servicio\":\"Corte\"}"}}]}}],
		  "usage": {"prompt_tokens": 50, "completion_tokens": 7, "prompt_tokens_details": {"cached_tokens": 30}}
		}`)
	}))
	defer srv.Close()

	p := New(Config{Name: "openai", BaseURL: srv.URL + "/v1/", APIKey: "sk-test", Model: "gpt-x", MaxTokensField: "max_completion_tokens"})
	resp, err := p.Chat(context.Background(), llm.Request{
		System: "sistema", MaxTokens: 100,
		Tools: []llm.Tool{{Name: "agendar", Strict: true, Schema: map[string]any{"type": "object"}}},
		Messages: []llm.Message{
			{Role: llm.RoleUser, Text: "hola"},
			{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "call_0", Name: "mis_reservas", Input: json.RawMessage(`{}`)}}},
			{Role: llm.RoleUser, ToolResults: []llm.ToolResult{{CallID: "call_0", Content: "no encontrada", IsError: true}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if resp.Stop != llm.StopToolUse || len(resp.Message.ToolCalls) != 1 || resp.Message.ToolCalls[0].Name != "agendar" {
		t.Errorf("resp = %+v", resp)
	}
	if resp.Message.Text != "Reviso." {
		t.Errorf("no se quitó <think>: %q", resp.Message.Text)
	}
	if resp.Usage != (llm.Usage{In: 50, Out: 7, CacheRead: 30}) {
		t.Errorf("usage = %+v", resp.Usage)
	}
	if auth != "Bearer sk-test" {
		t.Errorf("auth = %q", auth)
	}

	if body["max_completion_tokens"] != float64(100) {
		t.Errorf("falta max_completion_tokens: %v", body)
	}
	msgs := body["messages"].([]any)
	roles := []string{}
	for _, m := range msgs {
		roles = append(roles, m.(map[string]any)["role"].(string))
	}
	if want := []string{"system", "user", "assistant", "tool"}; len(roles) != 4 || roles[0] != want[0] || roles[3] != want[3] {
		t.Errorf("roles = %v, want %v", roles, want)
	}
	tool := msgs[3].(map[string]any)
	if tool["tool_call_id"] != "call_0" || tool["content"] != "ERROR: no encontrada" {
		t.Errorf("mensaje tool = %v", tool)
	}
	fn := body["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "agendar" || fn["strict"] != true {
		t.Errorf("tool = %v", fn)
	}
}

func TestErrorHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error": {"message": "modelo inexistente"}}`)
	}))
	defer srv.Close()
	p := New(Config{Name: "ollama", BaseURL: srv.URL, Model: "x"})
	_, err := p.Chat(context.Background(), llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Text: "hola"}}})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 400 {
		t.Fatalf("err = %v", err)
	}
}

// Gemini exige que cada tool call vuelva con su thought_signature, que viaja en
// extra_content. El adaptador debe reenviar el mensaje del asistente intacto.
func TestReenviaExtraContent(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var b map[string]any
		json.Unmarshal(raw, &b)
		bodies = append(bodies, b)
		if len(bodies) == 1 {
			io.WriteString(w, `{"choices": [{"finish_reason": "tool_calls", "message": {"role": "assistant",
			  "tool_calls": [{"id": "call_1", "type": "function",
			    "extra_content": {"google": {"thought_signature": "firma-gemini"}},
			    "function": {"name": "hora", "arguments": "{}"}}]}}]}`)
			return
		}
		io.WriteString(w, `{"choices": [{"finish_reason": "stop", "message": {"role": "assistant", "content": "Son las 10."}}]}`)
	}))
	defer srv.Close()

	p := New(Config{Name: "gemini", BaseURL: srv.URL, Model: "x"})
	ctx := context.Background()
	req := llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Text: "¿hora?"}}}
	r1, err := p.Chat(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	req.Messages = append(req.Messages, r1.Message, llm.Message{
		Role: llm.RoleUser, ToolResults: []llm.ToolResult{{CallID: "call_1", Content: "10:00"}},
	})
	if _, err := p.Chat(ctx, req); err != nil {
		t.Fatal(err)
	}
	asist, _ := json.Marshal(bodies[1]["messages"].([]any)[2])
	if !strings.Contains(string(asist), `"thought_signature":"firma-gemini"`) {
		t.Errorf("no se reenvió la firma: %s", asist)
	}
}

func TestNoRetryY5xxTemporal(t *testing.T) {
	llamadas := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		llamadas++
		w.WriteHeader(http.StatusServiceUnavailable)
		io.WriteString(w, `{"error": {"message": "high demand"}}`)
	}))
	defer srv.Close()
	p := New(Config{Name: "gemini", BaseURL: srv.URL, Model: "x", NoRetry: true})
	_, err := p.Chat(context.Background(), llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Text: "hola"}}})
	if !llm.IsTemporary(err) || llamadas != 1 {
		t.Fatalf("err = %v, llamadas = %d; se esperaba un error temporal sin reintento", err, llamadas)
	}
}
