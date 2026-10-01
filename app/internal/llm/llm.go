package llm

import (
	"context"
	"encoding/json"
	"errors"
)

// Provider es un modelo de lenguaje con soporte de herramientas. El motor de
// conversación solo conoce esta interfaz; cada proveedor traduce a su API.
type Provider interface {
	Chat(ctx context.Context, req Request) (Response, error)
	// Name identifica proveedor y modelo en logs, p. ej. "anthropic/claude-opus-5".
	Name() string
}

type Request struct {
	// System debe ser estable entre llamadas para aprovechar el cache de prompts de
	// los proveedores que lo tienen.
	System    string
	Tools     []Tool
	Messages  []Message
	MaxTokens int
}

// Tool describe una herramienta con un JSON Schema de tipo object.
type Tool struct {
	Name        string
	Description string
	Schema      map[string]any
	// Strict pide al proveedor que el input cumpla el schema exactamente. Los que no
	// lo soportan lo ignoran; el código igual valida el input.
	Strict bool
}

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message es un turno de la conversación. Un turno de usuario lleva Text o
// ToolResults; uno de asistente lleva Text y/o ToolCalls.
type Message struct {
	Role        Role
	Text        string
	ToolCalls   []ToolCall
	ToolResults []ToolResult
	// Native es la representación propia del proveedor que produjo el mensaje. Si
	// el mismo proveedor lo recibe de vuelta, lo usa tal cual (p. ej. Anthropic
	// necesita sus bloques de thinking dentro del loop de herramientas). Otro
	// proveedor lo ignora y usa los campos comunes.
	Native any
}

type ToolCall struct {
	ID    string
	Name  string
	Input json.RawMessage
}

type ToolResult struct {
	CallID  string
	Content string
	IsError bool
}

type StopReason string

const (
	StopEnd       StopReason = "end"
	StopToolUse   StopReason = "tool_use"
	StopMaxTokens StopReason = "max_tokens"
	// StopRefusal: el proveedor se negó a responder por políticas de seguridad.
	StopRefusal StopReason = "refusal"
)

type Response struct {
	// Message es el turno del asistente, listo para agregarlo al historial.
	Message Message
	Stop    StopReason
	// Detail amplía Stop cuando el proveedor lo informa (p. ej. la categoría de un
	// refusal).
	Detail string
	Usage  Usage
}

// Usage son los tokens de una llamada. In incluye los tokens leídos y escritos en
// cache; CacheRead es la parte leída de cache.
type Usage struct {
	In, Out, CacheRead int64
}

func (u *Usage) Add(o Usage) {
	u.In += o.In
	u.Out += o.Out
	u.CacheRead += o.CacheRead
}

// TemporaryError marca un error del proveedor que puede resolverse reintentando
// (sobrecarga, 5xx). Los adaptadores lo usan para que quien reintente, p. ej. el
// limitador de cuota, no tenga que conocer los tipos de error de cada API.
type TemporaryError struct{ Err error }

func (e *TemporaryError) Error() string { return e.Err.Error() }
func (e *TemporaryError) Unwrap() error { return e.Err }

func IsTemporary(err error) bool {
	var t *TemporaryError
	return errors.As(err, &t)
}
