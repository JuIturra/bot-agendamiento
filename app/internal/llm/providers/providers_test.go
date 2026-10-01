package providers

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	casos := []struct{ spec, provider, model string }{
		{"claude-opus-5", "anthropic", "claude-opus-5"},
		{"anthropic/claude-sonnet-5", "anthropic", "claude-sonnet-5"},
		{"openai/gpt-5", "openai", "gpt-5"},
		{"gemini/gemini-3.6-flash", "gemini", "gemini-3.6-flash"},
		{"ollama/qwen3:8b", "ollama", "qwen3:8b"},
		{"ollama/hf.co/org/modelo", "ollama", "hf.co/org/modelo"},
	}
	for _, c := range casos {
		p, m, err := Parse(c.spec)
		if err != nil || p != c.provider || m != c.model {
			t.Errorf("Parse(%q) = %q, %q, %v", c.spec, p, m, err)
		}
	}
	if _, _, err := Parse("gemini"); err == nil || !strings.Contains(err.Error(), "gemini/gemini-3.6-flash") {
		t.Errorf(`Parse("gemini") debería pedir el modelo con un ejemplo: %v`, err)
	}
	for _, mal := range []string{"", "ollama/", "desconocido/modelo"} {
		if _, _, err := Parse(mal); err == nil {
			t.Errorf("Parse(%q) debería fallar", mal)
		}
	}
}

func TestNewPideCredenciales(t *testing.T) {
	if _, err := New("anthropic/claude-opus-5", Credentials{}); err == nil || !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") {
		t.Errorf("err = %v", err)
	}
	if _, err := New("gemini/gemini-3.6-flash", Credentials{}); err == nil || !strings.Contains(err.Error(), "GEMINI_API_KEY") {
		t.Errorf("gemini sin clave: err = %v", err)
	}
	if _, err := New("openai-compatible/x", Credentials{}); err == nil {
		t.Error("openai-compatible sin LLM_BASE_URL debería fallar")
	}
	// Ollama local no necesita clave.
	p, err := New("ollama/qwen3:8b", Credentials{})
	if err != nil || p.Name() != "ollama/qwen3:8b" {
		t.Errorf("ollama: %v, %v", p, err)
	}
}
