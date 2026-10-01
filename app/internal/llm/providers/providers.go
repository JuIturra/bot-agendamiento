// Package providers crea un llm.Provider a partir de un identificador
// "proveedor/modelo", p. ej. "anthropic/claude-opus-5", "openai/gpt-5" u
// "ollama/qwen3:8b".
package providers

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"agendamiento/internal/llm"
	"agendamiento/internal/llm/anthropic"
	"agendamiento/internal/llm/openai"
)

// Credentials son las claves y URLs disponibles en el entorno.
type Credentials struct {
	AnthropicAPIKey string
	OpenAIAPIKey    string
	GeminiAPIKey    string
	// BaseURL pisa la URL por defecto del proveedor elegido (p. ej. Ollama en otra
	// máquina, o un servidor OpenAI-compatible cualquiera).
	BaseURL string
	// APIKey genérica para servidores OpenAI-compatible.
	APIKey string
	// NoRetry desactiva los reintentos propios de los adaptadores.
	NoRetry bool
}

var ejemplos = map[string]string{
	"anthropic":         "anthropic/claude-opus-5",
	"openai":            "openai/gpt-5",
	"gemini":            "gemini/gemini-3.6-flash",
	"ollama":            "ollama/qwen3:8b",
	"openai-compatible": "openai-compatible/llama-3.3-70b-versatile",
}

// DefaultProvider se usa cuando el identificador no trae prefijo.
const DefaultProvider = "anthropic"

type factory func(model string, c Credentials) (llm.Provider, error)

var registry = map[string]factory{
	"anthropic": func(model string, c Credentials) (llm.Provider, error) {
		if c.AnthropicAPIKey == "" {
			return nil, fmt.Errorf("falta ANTHROPIC_API_KEY")
		}
		return anthropic.New(anthropic.Config{APIKey: c.AnthropicAPIKey, Model: model, BaseURL: c.BaseURL, NoRetry: c.NoRetry}), nil
	},
	"openai": func(model string, c Credentials) (llm.Provider, error) {
		if c.OpenAIAPIKey == "" {
			return nil, fmt.Errorf("falta OPENAI_API_KEY")
		}
		return openai.New(openai.Config{
			Name: "openai", BaseURL: or(c.BaseURL, "https://api.openai.com/v1"),
			APIKey: c.OpenAIAPIKey, Model: model, MaxTokensField: "max_completion_tokens", NoRetry: c.NoRetry,
		}), nil
	},
	// Gemini por su endpoint compatible con OpenAI (en beta según Google).
	"gemini": func(model string, c Credentials) (llm.Provider, error) {
		if c.GeminiAPIKey == "" {
			return nil, fmt.Errorf("falta GEMINI_API_KEY")
		}
		return openai.New(openai.Config{
			Name: "gemini", BaseURL: or(c.BaseURL, "https://generativelanguage.googleapis.com/v1beta/openai"),
			APIKey: c.GeminiAPIKey, Model: model, NoRetry: c.NoRetry,
		}), nil
	},
	"ollama": func(model string, c Credentials) (llm.Provider, error) {
		return openai.New(openai.Config{
			Name: "ollama", BaseURL: or(c.BaseURL, "http://localhost:11434/v1"),
			APIKey: c.APIKey, Model: model, Timeout: 5 * time.Minute, NoRetry: c.NoRetry,
		}), nil
	},
	// Cualquier servidor con la API Chat Completions: Groq, OpenRouter, DeepSeek,
	// vLLM, LM Studio... Requiere LLM_BASE_URL.
	"openai-compatible": func(model string, c Credentials) (llm.Provider, error) {
		if c.BaseURL == "" {
			return nil, fmt.Errorf("openai-compatible requiere LLM_BASE_URL")
		}
		return openai.New(openai.Config{
			Name: "openai-compatible", BaseURL: c.BaseURL, APIKey: c.APIKey, Model: model, NoRetry: c.NoRetry,
		}), nil
	},
}

// Parse separa "proveedor/modelo". Sin prefijo se usa el proveedor por defecto.
// Solo se corta en la primera barra, porque hay modelos con barras en el nombre
// (p. ej. "ollama/hf.co/org/modelo").
func Parse(spec string) (provider, model string, err error) {
	spec = strings.TrimSpace(spec)
	provider, model, ok := strings.Cut(spec, "/")
	if !ok {
		// "gemini" a secas es un proveedor sin modelo, no un modelo de Anthropic.
		if _, esProveedor := registry[spec]; esProveedor {
			return "", "", fmt.Errorf("%q es un proveedor, falta el modelo: usar %s/<modelo>, p. ej. %s", spec, spec, ejemplos[spec])
		}
		provider, model = DefaultProvider, spec
	}
	if _, known := registry[provider]; !known {
		return "", "", fmt.Errorf("proveedor desconocido %q en %q (disponibles: %s)",
			provider, spec, strings.Join(Names(), ", "))
	}
	if model == "" {
		return "", "", fmt.Errorf("modelo vacío en %q", spec)
	}
	return provider, model, nil
}

// New crea el proveedor para spec.
func New(spec string, c Credentials) (llm.Provider, error) {
	provider, model, err := Parse(spec)
	if err != nil {
		return nil, err
	}
	p, err := registry[provider](model, c)
	if err != nil {
		return nil, fmt.Errorf("proveedor %s: %w", provider, err)
	}
	return p, nil
}

// Names lista los proveedores registrados.
func Names() []string {
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
