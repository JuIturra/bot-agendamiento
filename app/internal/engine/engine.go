// Package engine procesa un mensaje del cliente: arma el contexto, conversa con
// Claude ejecutando herramientas y guarda el turno.
package engine

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"agendamiento/internal/llm"
	"agendamiento/internal/llm/limit"
	"agendamiento/internal/store"
	"agendamiento/internal/tenant"
	"agendamiento/internal/tools"
)

const (
	// maxIteraciones corta el loop de herramientas si el modelo no termina.
	maxIteraciones = 6
	// historial es cuántos turnos de texto previos se envían.
	historial = 20
	// inactividad tras la cual se abre una conversación nueva.
	inactividad = 24 * time.Hour
	maxTokens   = 2000
)

// Respuestas fijas para cuando el modelo no produce una utilizable.
const (
	msgNoPuedo  = "Disculpa, en eso no te puedo ayudar. ¿Te ayudo a agendar una hora o con alguna duda del local?"
	msgProblema = "Disculpa, tuve un problema para procesar tu mensaje. ¿Me lo puedes repetir?"
	// Cuota del proveedor agotada (p. ej. capa gratuita de Gemini): reintentar al
	// tiro no sirve.
	msgNoDisponible = "Disculpa, en este momento no puedo responder. ¿Me escribes de nuevo en un rato?"
)

// Inbound es un mensaje normalizado, venga del canal que venga.
type Inbound struct {
	Canal          string // "telegram", "consola", ...
	ExternalUserID string
	Nombre         string
	Texto          string
	Enviado        time.Time
}

type Engine struct {
	LLM      llm.Provider
	Store    *store.Store
	Tenant   *tenant.Config
	TenantID string
	Tools    *tools.Toolset
	Log      *slog.Logger

	system string
	defs   []llm.Tool
}

// Init precalcula el system prompt y las tools, que no cambian entre mensajes.
func (e *Engine) Init() {
	e.system = llm.SystemPrompt(e.Tenant)
	e.defs = e.Tools.Definitions()
}

// Handle procesa un mensaje y devuelve la respuesta para el cliente. Devuelve "" si
// el bot no debe responder (conversación tomada por una persona).
func (e *Engine) Handle(ctx context.Context, in Inbound) (string, error) {
	customerID, err := e.Store.UpsertCustomer(ctx, e.TenantID, in.Canal, in.ExternalUserID, in.Nombre)
	if err != nil {
		return "", fmt.Errorf("cliente: %w", err)
	}
	conv, err := e.Store.ActiveConversation(ctx, e.TenantID, customerID, inactividad)
	if err != nil {
		return "", fmt.Errorf("conversación: %w", err)
	}
	if conv.Estado == "humano" {
		return "", nil
	}
	prev, err := e.Store.RecentTextMessages(ctx, conv.ID, historial)
	if err != nil {
		return "", fmt.Errorf("historial: %w", err)
	}

	texto := llm.ConFecha(in.Texto, in.Enviado, e.Tenant.Location())
	if in.Nombre != "" && len(prev) == 0 {
		// Primer mensaje: se le da al modelo el nombre del perfil para que no tenga que
		// preguntarlo si el cliente no da otro.
		texto += fmt.Sprintf("\n(Nombre en el perfil del chat: %s)", in.Nombre)
	}
	if err := e.Store.AddMessage(ctx, conv.ID, "user", textContent(texto), store.Usage{}); err != nil {
		return "", err
	}

	msgs := buildHistory(append(prev, store.TextMessage{Rol: "user", Text: texto}))
	sess := tools.Session{CustomerID: customerID}
	log := e.Log.With("conversation", conv.ID, "llm", e.LLM.Name())

	respuesta, usage, err := e.loop(ctx, log, conv.ID, sess, msgs)
	if err != nil {
		log.Error("loop LLM", "err", err)
	}
	if err := e.Store.AddMessage(ctx, conv.ID, "assistant", textContent(respuesta), usage); err != nil {
		return "", err
	}
	log.Info("turno", "tokens_in", usage.In, "tokens_out", usage.Out, "cache_read", usage.CacheRead)
	return respuesta, nil
}

// loop conversa con el modelo hasta obtener una respuesta. Siempre devuelve un texto
// para el cliente, aunque también devuelva un error.
func (e *Engine) loop(ctx context.Context, log *slog.Logger, convID string, sess tools.Session, msgs []llm.Message) (string, store.Usage, error) {
	var usage llm.Usage
	// avisos confirma los cambios que ya hicieron las tools en este turno. Si el
	// modelo falla después, se envían en vez del mensaje genérico de error: la
	// reserva existe y el cliente tiene que saberlo.
	var avisos []string
	fallback := func(err error) string {
		switch {
		case len(avisos) > 0:
			return strings.Join(avisos, "\n") + "\n¿Te ayudo con algo más?"
		case limit.IsCuota(err):
			return msgNoDisponible
		default:
			return msgProblema
		}
	}
	for i := 0; i < maxIteraciones; i++ {
		resp, err := e.LLM.Chat(ctx, llm.Request{
			System: e.system, Tools: e.defs, Messages: msgs, MaxTokens: maxTokens,
		})
		if err != nil {
			return fallback(err), toStore(usage), err
		}
		usage.Add(resp.Usage)

		switch resp.Stop {
		case llm.StopRefusal:
			log.Warn("refusal", "detail", resp.Detail)
			return msgNoPuedo, toStore(usage), nil
		case llm.StopToolUse:
			// sigue abajo
		default:
			// end, max_tokens: se entrega el texto que haya.
			if resp.Message.Text != "" {
				return resp.Message.Text, toStore(usage), nil
			}
			log.Warn("respuesta sin texto", "stop", resp.Stop)
			return fallback(nil), toStore(usage), nil
		}

		// El turno del asistente se agrega completo antes de los resultados: los
		// proveedores lo exigen para continuar.
		msgs = append(msgs, resp.Message)
		results := make([]llm.ToolResult, 0, len(resp.Message.ToolCalls))
		for _, call := range resp.Message.ToolCalls {
			res, err := e.Tools.Execute(ctx, sess, call.Name, call.Input)
			if err != nil {
				log.Error("tool", "name", call.Name, "err", err)
				res = tools.Result{Content: "error interno al ejecutar la herramienta; intenta de nuevo más tarde", IsError: true}
			}
			log.Info("tool", "name", call.Name, "input", string(call.Input), "is_error", res.IsError)
			if res.Aviso != "" {
				avisos = append(avisos, res.Aviso)
			}
			if err := e.Store.AddMessage(ctx, convID, "tool", map[string]any{
				"name": call.Name, "input": call.Input, "result": res.Content, "is_error": res.IsError,
			}, store.Usage{}); err != nil {
				return fallback(err), toStore(usage), err
			}
			results = append(results, llm.ToolResult{CallID: call.ID, Content: res.Content, IsError: res.IsError})
		}
		// Todos los resultados van en un único turno.
		msgs = append(msgs, llm.Message{Role: llm.RoleUser, ToolResults: results})
	}
	log.Warn("se alcanzó el máximo de iteraciones")
	return fallback(nil), toStore(usage), nil
}

func toStore(u llm.Usage) store.Usage {
	return store.Usage{In: u.In, Out: u.Out, CacheRead: u.CacheRead}
}

// buildHistory convierte el historial de texto en mensajes. Los proveedores esperan
// que el primero sea del usuario y que los roles se alternen, así que se descartan
// asistentes iniciales y se juntan turnos seguidos del mismo rol.
func buildHistory(turnos []store.TextMessage) []llm.Message {
	var out []llm.Message
	var rol string
	var buf []string
	flush := func() {
		if len(buf) == 0 {
			return
		}
		out = append(out, llm.Message{Role: llm.Role(rol), Text: strings.Join(buf, "\n\n")})
		buf = nil
	}
	for _, t := range turnos {
		if t.Text == "" || (len(out) == 0 && len(buf) == 0 && t.Rol != "user") {
			continue
		}
		if t.Rol != rol {
			flush()
			rol = t.Rol
		}
		buf = append(buf, t.Text)
	}
	flush()
	return out
}

func textContent(s string) map[string]string { return map[string]string{"text": s} }
