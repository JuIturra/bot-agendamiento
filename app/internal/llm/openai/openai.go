// Package openai implementa llm.Provider sobre la API Chat Completions de OpenAI.
// La misma API la exponen Ollama, Groq, OpenRouter, DeepSeek, Gemini y otros, así
// que este adaptador sirve para todos cambiando BaseURL.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"agendamiento/internal/llm"
)

type Config struct {
	// Name es el prefijo para logs: "openai", "ollama", etc.
	Name    string
	BaseURL string // p. ej. https://api.openai.com/v1
	APIKey  string // vacío si el servidor no lo pide (Ollama local)
	Model   string
	// MaxTokensField es el nombre del parámetro de límite de salida. OpenAI usa
	// max_completion_tokens en sus modelos actuales; muchos servidores compatibles
	// solo entienden max_tokens.
	MaxTokensField string
	// Timeout por llamada. Los modelos locales pueden tardar bastante.
	Timeout time.Duration
	// NoRetry desactiva el reintento propio ante 429/5xx. Se usa cuando otro nivel
	// (el limitador de cuota) reintenta y necesita contar cada request.
	NoRetry bool
}

type Provider struct {
	cfg  Config
	http *http.Client
}

func New(c Config) *Provider {
	if c.MaxTokensField == "" {
		c.MaxTokensField = "max_tokens"
	}
	if c.Timeout == 0 {
		c.Timeout = 2 * time.Minute
	}
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	return &Provider{cfg: c, http: &http.Client{Timeout: c.Timeout}}
}

func (p *Provider) Name() string { return p.cfg.Name + "/" + p.cfg.Model }

// --- formato de la API ---

type chatMessage struct {
	Role       string     `json:"role"`
	Content    *string    `json:"content"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
	// ExtraContent son datos propios del proveedor que hay que devolver intactos.
	// Gemini pone aquí la thought_signature y rechaza con 400 la siguiente llamada
	// si falta.
	ExtraContent json.RawMessage `json:"extra_content,omitempty"`
}

type toolDef struct {
	Type     string      `json:"type"`
	Function functionDef `json:"function"`
}

type functionDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
	Strict      bool           `json:"strict,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message      chatMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens        int64 `json:"prompt_tokens"`
		CompletionTokens    int64 `json:"completion_tokens"`
		PromptTokensDetails struct {
			CachedTokens int64 `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
}

// APIError es una respuesta no exitosa del servidor.
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.Status, e.Body)
}

func (p *Provider) Chat(ctx context.Context, req llm.Request) (llm.Response, error) {
	body := map[string]any{
		"model":              p.cfg.Model,
		"messages":           messages(req),
		p.cfg.MaxTokensField: req.MaxTokens,
	}
	if len(req.Tools) > 0 {
		body["tools"] = tools(req.Tools)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return llm.Response{}, err
	}

	var resp chatResponse
	if err := p.post(ctx, raw, &resp); err != nil {
		return llm.Response{}, err
	}
	if len(resp.Choices) == 0 {
		return llm.Response{}, errors.New("respuesta sin choices")
	}
	choice := resp.Choices[0]

	// El mensaje original se guarda para reenviarlo tal cual dentro del loop de
	// tools, con los campos que el tipo común no modela (ver ExtraContent).
	msg := llm.Message{Role: llm.RoleAssistant, Native: choice.Message}
	if choice.Message.Content != nil {
		msg.Text = strings.TrimSpace(stripThink(*choice.Message.Content))
	}
	for _, c := range choice.Message.ToolCalls {
		args := c.Function.Arguments
		if strings.TrimSpace(args) == "" {
			args = "{}"
		}
		msg.ToolCalls = append(msg.ToolCalls, llm.ToolCall{
			ID: c.ID, Name: c.Function.Name, Input: json.RawMessage(args),
		})
	}

	out := llm.Response{
		Message: msg,
		Usage: llm.Usage{
			In:        resp.Usage.PromptTokens,
			Out:       resp.Usage.CompletionTokens,
			CacheRead: resp.Usage.PromptTokensDetails.CachedTokens,
		},
	}
	switch {
	case len(msg.ToolCalls) > 0:
		// Algunos servidores compatibles devuelven tool_calls con finish_reason
		// "stop"; lo que manda es que haya llamadas.
		out.Stop = llm.StopToolUse
	case choice.FinishReason == "length":
		out.Stop = llm.StopMaxTokens
	case choice.FinishReason == "content_filter":
		out.Stop = llm.StopRefusal
	default:
		out.Stop = llm.StopEnd
	}
	return out, nil
}

// post hace la llamada con un reintento ante 429 y 5xx, salvo que NoRetry esté
// activo. Un 5xx que no se resolvió vuelve como llm.TemporaryError.
func (p *Provider) post(ctx context.Context, body []byte, out any) error {
	intentos := 2
	if p.cfg.NoRetry {
		intentos = 1
	}
	var last error
	for intento := 0; intento < intentos; intento++ {
		if intento > 0 {
			select {
			case <-time.After(2 * time.Second):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.BaseURL+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		if p.cfg.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
		}
		resp, err := p.http.Do(req)
		if err != nil {
			last = err
			continue
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if err != nil {
			last = err
			continue
		}
		if resp.StatusCode >= 500 {
			last = &llm.TemporaryError{Err: &APIError{Status: resp.StatusCode, Body: truncar(string(data), 500)}}
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			last = &APIError{Status: resp.StatusCode, Body: truncar(string(data), 500)}
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return &APIError{Status: resp.StatusCode, Body: truncar(string(data), 500)}
		}
		return json.Unmarshal(data, out)
	}
	return last
}

func messages(req llm.Request) []chatMessage {
	out := []chatMessage{{Role: "system", Content: ptr(req.System)}}
	for _, m := range req.Messages {
		if m.Role == llm.RoleAssistant {
			if native, ok := m.Native.(chatMessage); ok {
				out = append(out, native)
				continue
			}
			cm := chatMessage{Role: "assistant"}
			if m.Text != "" {
				cm.Content = ptr(m.Text)
			}
			for _, c := range m.ToolCalls {
				tc := toolCall{ID: c.ID, Type: "function"}
				tc.Function.Name = c.Name
				tc.Function.Arguments = string(c.Input)
				cm.ToolCalls = append(cm.ToolCalls, tc)
			}
			out = append(out, cm)
			continue
		}
		// En esta API cada resultado de herramienta es un mensaje con rol "tool".
		// No hay campo para marcar errores, así que se indica en el texto.
		for _, r := range m.ToolResults {
			content := r.Content
			if r.IsError {
				content = "ERROR: " + content
			}
			out = append(out, chatMessage{Role: "tool", ToolCallID: r.CallID, Content: ptr(content)})
		}
		if m.Text != "" {
			out = append(out, chatMessage{Role: "user", Content: ptr(m.Text)})
		}
	}
	return out
}

func tools(ts []llm.Tool) []toolDef {
	out := make([]toolDef, len(ts))
	for i, t := range ts {
		out[i] = toolDef{Type: "function", Function: functionDef{
			Name: t.Name, Description: t.Description, Parameters: t.Schema, Strict: t.Strict,
		}}
	}
	return out
}

// Algunos modelos locales de razonamiento (qwen3, deepseek-r1) mandan su
// razonamiento dentro de <think>...</think> en el contenido.
var thinkRE = regexp.MustCompile(`(?s)<think>.*?</think>`)

func stripThink(s string) string { return thinkRE.ReplaceAllString(s, "") }

func ptr(s string) *string { return &s }

func truncar(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
