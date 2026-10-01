// Package anthropic implementa llm.Provider sobre la API de Claude.
package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"agendamiento/internal/llm"
)

type Config struct {
	APIKey string
	Model  string
	// BaseURL permite apuntar a un proxy o a un servidor de pruebas.
	BaseURL string
	// NoRetry desactiva los reintentos del SDK (ver openai.Config.NoRetry).
	NoRetry bool
}

type Provider struct {
	client anthropic.Client
	model  string
}

func New(c Config) *Provider {
	opts := []option.RequestOption{option.WithAPIKey(c.APIKey)}
	if c.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(c.BaseURL))
	}
	if c.NoRetry {
		opts = append(opts, option.WithMaxRetries(0))
	}
	return &Provider{client: anthropic.NewClient(opts...), model: c.Model}
}

func (p *Provider) Name() string { return "anthropic/" + p.model }

func (p *Provider) Chat(ctx context.Context, req llm.Request) (llm.Response, error) {
	resp, err := p.client.Messages.New(ctx, p.params(req), p.requestOptions()...)
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) && apiErr.StatusCode >= 500 {
		return llm.Response{}, &llm.TemporaryError{Err: err}
	}
	if err != nil {
		return llm.Response{}, err
	}
	out := llm.Response{
		Message: llm.Message{Role: llm.RoleAssistant, Native: resp.ToParam()},
		Usage: llm.Usage{
			In:        resp.Usage.InputTokens + resp.Usage.CacheCreationInputTokens + resp.Usage.CacheReadInputTokens,
			Out:       resp.Usage.OutputTokens,
			CacheRead: resp.Usage.CacheReadInputTokens,
		},
	}
	var texts []string
	for _, block := range resp.Content {
		switch b := block.AsAny().(type) {
		case anthropic.TextBlock:
			if t := strings.TrimSpace(b.Text); t != "" {
				texts = append(texts, t)
			}
		case anthropic.ToolUseBlock:
			out.Message.ToolCalls = append(out.Message.ToolCalls, llm.ToolCall{
				ID: b.ID, Name: b.Name, Input: json.RawMessage(b.JSON.Input.Raw()),
			})
		}
	}
	out.Message.Text = strings.Join(texts, "\n\n")

	switch resp.StopReason {
	case anthropic.StopReasonToolUse:
		out.Stop = llm.StopToolUse
	case anthropic.StopReasonMaxTokens:
		out.Stop = llm.StopMaxTokens
	case anthropic.StopReasonRefusal:
		out.Stop = llm.StopRefusal
		out.Detail = string(resp.StopDetails.Category)
	default:
		out.Stop = llm.StopEnd
	}
	return out, nil
}

func (p *Provider) params(req llm.Request) anthropic.MessageNewParams {
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(p.model),
		MaxTokens: int64(req.MaxTokens),
		System: []anthropic.TextBlockParam{{
			Text:         req.System,
			CacheControl: anthropic.NewCacheControlEphemeralParam(),
		}},
		Tools:    tools(req.Tools),
		Messages: messages(req.Messages),
		// Cachea también la conversación hasta el último bloque: en el loop de tools
		// cada llamada repite todo lo anterior.
		CacheControl: anthropic.NewCacheControlEphemeralParam(),
	}
	// Chat transaccional: thinking adaptativo con effort bajo. Haiku 4.5 no acepta
	// ninguno de los dos.
	if !strings.HasPrefix(p.model, "claude-haiku") {
		params.Thinking = anthropic.ThinkingConfigParamUnion{OfAdaptive: &anthropic.ThinkingConfigAdaptiveParam{}}
		params.OutputConfig = anthropic.OutputConfigParam{Effort: anthropic.OutputConfigEffortLow}
	}
	return params
}

// requestOptions activa el fallback del servidor ante un refusal en Opus 5: la API
// reintenta con otro modelo en la misma llamada en vez de cortar la conversación.
func (p *Provider) requestOptions() []option.RequestOption {
	if p.model != "claude-opus-5" {
		return nil
	}
	return []option.RequestOption{
		option.WithHeaderAdd("anthropic-beta", "server-side-fallback-2026-07-01"),
		option.WithJSONSet("fallbacks", "default"),
	}
}

func tools(ts []llm.Tool) []anthropic.ToolUnionParam {
	out := make([]anthropic.ToolUnionParam, len(ts))
	for i, t := range ts {
		schema := anthropic.ToolInputSchemaParam{Properties: t.Schema["properties"]}
		if req, ok := t.Schema["required"].([]string); ok {
			schema.Required = req
		}
		extra := map[string]any{}
		for k, v := range t.Schema {
			if k != "type" && k != "properties" && k != "required" {
				extra[k] = v
			}
		}
		if len(extra) > 0 {
			schema.ExtraFields = extra
		}
		tp := anthropic.ToolParam{
			Name:        t.Name,
			Description: anthropic.String(t.Description),
			InputSchema: schema,
		}
		if t.Strict {
			tp.Strict = anthropic.Bool(true)
		}
		out[i] = anthropic.ToolUnionParam{OfTool: &tp}
	}
	return out
}

func messages(ms []llm.Message) []anthropic.MessageParam {
	out := make([]anthropic.MessageParam, 0, len(ms))
	for _, m := range ms {
		// Un turno producido por este proveedor se reenvía tal cual, con sus bloques
		// de thinking: la API los exige al continuar un loop de herramientas.
		if native, ok := m.Native.(anthropic.MessageParam); ok {
			out = append(out, native)
			continue
		}
		var blocks []anthropic.ContentBlockParamUnion
		for _, r := range m.ToolResults {
			blocks = append(blocks, anthropic.NewToolResultBlock(r.CallID, r.Content, r.IsError))
		}
		if m.Text != "" {
			blocks = append(blocks, anthropic.NewTextBlock(m.Text))
		}
		for _, c := range m.ToolCalls {
			blocks = append(blocks, anthropic.NewToolUseBlock(c.ID, c.Input, c.Name))
		}
		if m.Role == llm.RoleAssistant {
			out = append(out, anthropic.NewAssistantMessage(blocks...))
		} else {
			out = append(out, anthropic.NewUserMessage(blocks...))
		}
	}
	return out
}
