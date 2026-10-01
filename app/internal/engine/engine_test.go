package engine

import (
	"testing"

	"agendamiento/internal/llm"
	"agendamiento/internal/store"
)

func TestBuildHistory(t *testing.T) {
	got := buildHistory([]store.TextMessage{
		{Rol: "assistant", Text: "quedó cortado por el límite"},
		{Rol: "user", Text: "hola"},
		{Rol: "user", Text: "quería hora"},
		{Rol: "assistant", Text: "¿para qué día?"},
		{Rol: "user", Text: ""},
		{Rol: "user", Text: "mañana"},
	})
	want := []llm.Message{
		{Role: llm.RoleUser, Text: "hola\n\nquería hora"},
		{Role: llm.RoleAssistant, Text: "¿para qué día?"},
		{Role: llm.RoleUser, Text: "mañana"},
	}
	if len(got) != len(want) {
		t.Fatalf("se esperaban %d mensajes alternados, hay %d: %+v", len(want), len(got), got)
	}
	for i := range want {
		if got[i].Role != want[i].Role || got[i].Text != want[i].Text {
			t.Errorf("mensaje %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}
