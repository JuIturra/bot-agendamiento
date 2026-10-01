// Package tools define las herramientas que Claude puede llamar y las ejecuta.
// Ninguna herramienta recibe tenant ni cliente desde el modelo: salen de Session,
// que arma el motor a partir del canal por el que llegó el mensaje.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"agendamiento/internal/booking"
	"agendamiento/internal/formato"
	"agendamiento/internal/llm"
)

// Session identifica a quién atiende el motor en este turno.
type Session struct {
	CustomerID string
}

// Result es la respuesta de una herramienta para el modelo.
type Result struct {
	Content string
	IsError bool
	// Aviso es un texto para el cliente que confirma un cambio ya hecho (reserva o
	// cancelación). Si el modelo falla después de la herramienta, el motor lo envía
	// igual, para que el cliente no crea que no pasó nada.
	Aviso string
}

// maxSlots limita cuántos horarios se devuelven al modelo por consulta.
const maxSlots = 30

// Toolset es el conjunto de herramientas de un negocio.
type Toolset struct {
	Booking *booking.Service
}

// Definitions devuelve las definiciones para la API. El orden y el contenido son
// fijos para un mismo negocio, así no se invalida el cache de prompts.
func (t *Toolset) Definitions() []llm.Tool {
	servicios := t.Booking.Cfg.NombresServicios()
	defs := []llm.Tool{
		{
			Name: "consultar_disponibilidad",
			Description: "Busca horarios libres para un servicio entre dos fechas (ambas incluidas, máximo 14 días). " +
				"Devuelve los horarios agrupados por día. Úsala siempre antes de ofrecer horarios.",
			Schema: schema(map[string]any{
				"servicio":    map[string]any{"type": "string", "enum": servicios},
				"fecha_desde": map[string]any{"type": "string", "description": "AAAA-MM-DD"},
				"fecha_hasta": map[string]any{"type": "string", "description": "AAAA-MM-DD"},
				"franja": map[string]any{
					"type": "string", "enum": []string{"mañana", "tarde", "cualquiera"},
					"description": "mañana = antes de las 13:00",
				},
			}),
		},
		{
			Name: "agendar",
			Description: "Reserva una hora para el cliente actual. Usa un horario devuelto por consultar_disponibilidad. " +
				"La reserva solo existe si la respuesta empieza con OK.",
			Schema: schema(map[string]any{
				"servicio":       map[string]any{"type": "string", "enum": servicios},
				"inicio":         map[string]any{"type": "string", "description": "AAAA-MM-DD HH:MM, hora local del negocio"},
				"nombre_cliente": map[string]any{"type": "string", "description": "Nombre con el que queda la reserva"},
			}),
		},
		{
			Name:        "mis_reservas",
			Description: "Lista las reservas futuras confirmadas del cliente actual, con su id.",
			Schema:      schema(map[string]any{}),
		},
		{
			Name:        "cancelar_reserva",
			Description: "Cancela una reserva del cliente actual. El id sale de mis_reservas.",
			Schema: schema(map[string]any{
				"reserva_id": map[string]any{"type": "string"},
			}),
		},
	}
	for i := range defs {
		defs[i].Strict = true
	}
	return defs
}

// schema arma un JSON Schema estricto: todas las propiedades obligatorias y sin
// propiedades adicionales. required se ordena para que el JSON sea estable.
func schema(props map[string]any) map[string]any {
	required := make([]string, 0, len(props))
	for k := range props {
		required = append(required, k)
	}
	slices.Sort(required)
	return map[string]any{
		"type":                 "object",
		"properties":           props,
		"required":             required,
		"additionalProperties": false,
	}
}

// Execute corre una herramienta. Los errores esperables (horario ocupado, fecha mal
// escrita) vuelven como Result con IsError para que el modelo los explique; solo los
// errores de infraestructura se devuelven como error.
func (t *Toolset) Execute(ctx context.Context, s Session, name string, input json.RawMessage) (Result, error) {
	switch name {
	case "consultar_disponibilidad":
		return t.consultar(ctx, input)
	case "agendar":
		return t.agendar(ctx, s, input)
	case "mis_reservas":
		return t.misReservas(ctx, s)
	case "cancelar_reserva":
		return t.cancelar(ctx, s, input)
	default:
		return fallo("herramienta desconocida: %s", name), nil
	}
}

func (t *Toolset) consultar(ctx context.Context, input json.RawMessage) (Result, error) {
	var in struct {
		Servicio   string `json:"servicio"`
		FechaDesde string `json:"fecha_desde"`
		FechaHasta string `json:"fecha_hasta"`
		Franja     string `json:"franja"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return fallo("input inválido: %v", err), nil
	}
	loc := t.Booking.Cfg.Location()
	desde, err1 := time.ParseInLocation(time.DateOnly, in.FechaDesde, loc)
	hasta, err2 := time.ParseInLocation(time.DateOnly, in.FechaHasta, loc)
	if err1 != nil || err2 != nil {
		return fallo("las fechas deben tener formato AAAA-MM-DD"), nil
	}
	slots, err := t.Booking.Disponibilidad(ctx, in.Servicio, desde, hasta, booking.Franja(in.Franja))
	if errors.Is(err, booking.ErrServicio) || errors.Is(err, booking.ErrRango) {
		return fallo("%v", err), nil
	}
	if err != nil {
		return Result{}, err
	}
	if len(slots) == 0 {
		return Result{Content: "No hay horarios libres en ese rango. Prueba con otras fechas u otra franja."}, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Horarios libres para %s:\n", in.Servicio)
	dia := ""
	for i, s := range slots {
		if i == maxSlots {
			b.WriteString("\n(hay más horarios; acota el rango si hace falta)")
			break
		}
		if d := s.Format(time.DateOnly); d != dia {
			if dia != "" {
				b.WriteString("\n")
			}
			dia = d
			fmt.Fprintf(&b, "%s (%s): ", d, formato.DiaSemana(s))
		} else {
			b.WriteString(", ")
		}
		b.WriteString(s.Format("15:04"))
	}
	return Result{Content: b.String()}, nil
}

func (t *Toolset) agendar(ctx context.Context, s Session, input json.RawMessage) (Result, error) {
	var in struct {
		Servicio      string `json:"servicio"`
		Inicio        string `json:"inicio"`
		NombreCliente string `json:"nombre_cliente"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return fallo("input inválido: %v", err), nil
	}
	inicio, err := parseInicio(in.Inicio, t.Booking.Cfg.Location())
	if err != nil {
		return fallo("inicio debe tener formato AAAA-MM-DD HH:MM"), nil
	}
	if strings.TrimSpace(in.NombreCliente) == "" {
		return fallo("falta el nombre del cliente"), nil
	}
	r, err := t.Booking.Agendar(ctx, s.CustomerID, in.Servicio, inicio, strings.TrimSpace(in.NombreCliente))
	switch {
	case errors.Is(err, booking.ErrOcupado), errors.Is(err, booking.ErrFueraDeHorario),
		errors.Is(err, booking.ErrMuyPronto), errors.Is(err, booking.ErrMuyLejos):
		return fallo("no se pudo agendar: %v. Consulta la disponibilidad de nuevo y ofrece otras opciones.", err), nil
	case errors.Is(err, booking.ErrServicio):
		return fallo("%v", err), nil
	case err != nil:
		return Result{}, err
	}
	fecha := formato.FechaHora(r.Inicio.In(t.Booking.Cfg.Location()))
	return Result{
		Content: fmt.Sprintf("OK. Reserva confirmada.\nid: %s\nservicio: %s\nfecha: %s\nnombre: %s",
			r.ID, r.Servicio, fecha, r.NombreCliente),
		Aviso: fmt.Sprintf("Tu hora quedó reservada: %s, el %s, a nombre de %s.", r.Servicio, fecha, r.NombreCliente),
	}, nil
}

func (t *Toolset) misReservas(ctx context.Context, s Session) (Result, error) {
	rs, err := t.Booking.MisReservas(ctx, s.CustomerID)
	if err != nil {
		return Result{}, err
	}
	if len(rs) == 0 {
		return Result{Content: "El cliente no tiene reservas futuras."}, nil
	}
	var b strings.Builder
	b.WriteString("Reservas futuras del cliente:\n")
	for _, r := range rs {
		fmt.Fprintf(&b, "- id %s: %s, %s, a nombre de %s\n",
			r.ID, r.Servicio, formato.FechaHora(r.Inicio.In(t.Booking.Cfg.Location())), r.NombreCliente)
	}
	return Result{Content: b.String()}, nil
}

func (t *Toolset) cancelar(ctx context.Context, s Session, input json.RawMessage) (Result, error) {
	var in struct {
		ReservaID string `json:"reserva_id"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return fallo("input inválido: %v", err), nil
	}
	r, err := t.Booking.Cancelar(ctx, s.CustomerID, strings.TrimSpace(in.ReservaID))
	if errors.Is(err, booking.ErrNoEncontrada) {
		return fallo("no hay una reserva confirmada con ese id para este cliente. Usa mis_reservas."), nil
	}
	if err != nil {
		return Result{}, err
	}
	fecha := formato.FechaHora(r.Inicio.In(t.Booking.Cfg.Location()))
	return Result{
		Content: fmt.Sprintf("OK. Reserva cancelada: %s, %s.", r.Servicio, fecha),
		Aviso:   fmt.Sprintf("Tu hora de %s del %s quedó cancelada.", r.Servicio, fecha),
	}, nil
}

func parseInicio(s string, loc *time.Location) (time.Time, error) {
	s = strings.Replace(strings.TrimSpace(s), "T", " ", 1)
	return time.ParseInLocation("2006-01-02 15:04", s, loc)
}

func fallo(format string, args ...any) Result {
	return Result{Content: fmt.Sprintf(format, args...), IsError: true}
}
