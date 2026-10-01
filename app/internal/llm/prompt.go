// Package llm define la interfaz común a los proveedores de modelos (llm.go) y arma
// el contenido que se les envía: el system prompt de cada negocio y el prefijo de
// fecha de los mensajes (este archivo). Los adaptadores viven en subpaquetes.
package llm

import (
	"fmt"
	"strings"
	"time"

	"agendamiento/internal/formato"
	"agendamiento/internal/tenant"
)

// SystemPrompt describe el rol del asistente y todo lo que sabe del negocio. Debe ser
// determinista: el mismo Config produce exactamente el mismo texto, para que el cache
// de prompts funcione. Por eso no incluye la fecha actual (va en cada mensaje).
func SystemPrompt(c *tenant.Config) string {
	var b strings.Builder
	fmt.Fprintf(&b, `Eres el asistente de %s y atiendes a sus clientes por chat (Telegram o WhatsApp).

Tu trabajo: responder dudas sobre el negocio, agendar horas, mostrar las reservas del cliente y cancelarlas.

Cómo responder:
- Escribe en español de Chile, cordial y directo, como una persona que atiende el chat del local. Mensajes cortos: esto se lee en el teléfono.
- No uses formato Markdown (ni asteriscos ni títulos). Puedes usar saltos de línea y listas simples con guiones.
- Responde solo con la información de este mensaje. Si te preguntan algo que no está aquí, di que no tienes ese dato y ofrece que el local lo confirme; no lo inventes.
- Si el cliente habla de algo que no tiene relación con el negocio, respóndele amablemente que solo puedes ayudar con temas del local.

Cómo agendar:
- Los horarios libres salen solo de la herramienta consultar_disponibilidad. Nunca ofrezcas un horario que no te haya devuelto.
- Ofrece pocas opciones (dos a cuatro) y pregunta cuál prefiere. Si el cliente pide un día u horario específico, busca ese.
- Antes de agendar necesitas el servicio, el horario elegido y el nombre del cliente.
- La reserva existe solo cuando agendar responde OK. No digas que quedó agendada antes de eso. Si agendar falla, explica el motivo y ofrece alternativas.
- Para cancelar, primero usa mis_reservas para identificar la reserva y confirma con el cliente cuál quiere cancelar.
- Cada mensaje del cliente empieza con la fecha y hora en que lo envió. Úsala para entender "hoy", "mañana" o "el viernes".

`, c.Nombre)

	b.WriteString("# El negocio\n\n")
	fmt.Fprintf(&b, "Nombre: %s\n", c.Nombre)
	if c.Descripcion != "" {
		fmt.Fprintf(&b, "Descripción: %s\n", strings.TrimSpace(c.Descripcion))
	}
	if c.Direccion != "" {
		fmt.Fprintf(&b, "Dirección: %s\n", c.Direccion)
	}
	if c.Telefono != "" {
		fmt.Fprintf(&b, "Teléfono: %s\n", c.Telefono)
	}
	if len(c.MediosPago) > 0 {
		fmt.Fprintf(&b, "Medios de pago: %s\n", strings.Join(c.MediosPago, ", "))
	}

	b.WriteString("\n# Horario de atención\n\n")
	dias := []struct {
		nombre string
		rangos []string
	}{
		{"Lunes", c.Horario.Lunes}, {"Martes", c.Horario.Martes},
		{"Miércoles", c.Horario.Miercoles}, {"Jueves", c.Horario.Jueves},
		{"Viernes", c.Horario.Viernes}, {"Sábado", c.Horario.Sabado},
		{"Domingo", c.Horario.Domingo},
	}
	for _, d := range dias {
		h := "cerrado"
		if len(d.rangos) > 0 {
			h = strings.Join(d.rangos, " y ")
		}
		fmt.Fprintf(&b, "- %s: %s\n", d.nombre, h)
	}
	if len(c.Feriados) > 0 {
		fmt.Fprintf(&b, "Cerrado también estos días: %s\n", strings.Join(c.Feriados, ", "))
	}
	fmt.Fprintf(&b, "Se agenda con al menos %d minutos de anticipación y hasta %d días hacia adelante.\n",
		c.AnticipacionMinimaMinutos, c.DiasMaximosAdelanto)

	b.WriteString("\n# Servicios y precios\n\n")
	for _, s := range c.Servicios {
		fmt.Fprintf(&b, "- %s: %s, dura %d minutos.", s.Nombre, formato.Precio(s.Precio), s.DuracionMinutos)
		if s.Descripcion != "" {
			fmt.Fprintf(&b, " %s", strings.TrimSpace(s.Descripcion))
		}
		b.WriteString("\n")
	}

	if len(c.PreguntasFrecuentes) > 0 {
		b.WriteString("\n# Preguntas frecuentes\n\n")
		for _, f := range c.PreguntasFrecuentes {
			fmt.Fprintf(&b, "P: %s\nR: %s\n\n", strings.TrimSpace(f.Pregunta), strings.TrimSpace(f.Respuesta))
		}
	}

	if s := strings.TrimSpace(c.InstruccionesExtra); s != "" {
		b.WriteString("\n# Indicaciones del dueño\n\n")
		b.WriteString(s)
		b.WriteString("\n")
	}
	return b.String()
}

// ConFecha antepone al texto del cliente la fecha y hora en que lo envió, en la zona
// del negocio. Se guarda así en el historial, para que el prefijo de mensajes no
// cambie entre turnos.
func ConFecha(texto string, enviado time.Time, loc *time.Location) string {
	return fmt.Sprintf("[%s]\n%s", formato.FechaHora(enviado.In(loc)), texto)
}
