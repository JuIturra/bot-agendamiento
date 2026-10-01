// Package calendar conecta con Google Calendar usando una cuenta de servicio. El
// dueño del negocio comparte su calendario con el email de la cuenta; no hay OAuth.
//
// Calendar no es la fuente de verdad (lo es Postgres): se usa para mostrarle las
// reservas al dueño donde ya mira su agenda y para leer los bloqueos que pone a mano.
package calendar

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	gcal "google.golang.org/api/calendar/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"

	"agendamiento/internal/booking"
)

type Client struct {
	svc *gcal.Service
}

// New crea un cliente a partir del JSON de una cuenta de servicio.
func New(ctx context.Context, credentialsJSON []byte, opts ...option.ClientOption) (*Client, error) {
	opts = append([]option.ClientOption{
		option.WithAuthCredentialsJSON(option.ServiceAccount, credentialsJSON),
		option.WithScopes(gcal.CalendarEventsScope, gcal.CalendarFreebusyScope),
	}, opts...)
	return newClient(ctx, opts...)
}

func newClient(ctx context.Context, opts ...option.ClientOption) (*Client, error) {
	svc, err := gcal.NewService(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("google calendar: %w", err)
	}
	return &Client{svc: svc}, nil
}

// Busy devuelve los tramos ocupados del calendario entre desde y hasta. Incluye los
// eventos que creó el propio bot; eso no molesta, porque coinciden con reservas que
// booking ya descuenta.
func (c *Client) Busy(ctx context.Context, calendarID string, desde, hasta time.Time) ([]booking.Interval, error) {
	resp, err := c.svc.Freebusy.Query(&gcal.FreeBusyRequest{
		TimeMin: desde.Format(time.RFC3339),
		TimeMax: hasta.Format(time.RFC3339),
		Items:   []*gcal.FreeBusyRequestItem{{Id: calendarID}},
	}).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("freebusy: %w", err)
	}
	cal, ok := resp.Calendars[calendarID]
	if !ok {
		return nil, fmt.Errorf("freebusy: la respuesta no incluye el calendario %s", calendarID)
	}
	// Un calendario no compartido con la cuenta de servicio no da error HTTP: viene
	// con errors "notFound" dentro de la respuesta.
	if len(cal.Errors) > 0 {
		return nil, fmt.Errorf("freebusy %s: %s (¿el calendario está compartido con la cuenta de servicio?)",
			calendarID, cal.Errors[0].Reason)
	}
	out := make([]booking.Interval, 0, len(cal.Busy))
	for _, b := range cal.Busy {
		ini, err1 := time.Parse(time.RFC3339, b.Start)
		fin, err2 := time.Parse(time.RFC3339, b.End)
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("freebusy: tramo inválido %q-%q", b.Start, b.End)
		}
		out = append(out, booking.Interval{Start: ini, End: fin})
	}
	return out, nil
}

// Event es lo que se escribe en el calendario por cada reserva.
type Event struct {
	// ID se deriva del id de la reserva, así crear dos veces el mismo evento no lo
	// duplica: la segunda vez Google responde 409 y se toma como éxito.
	ID          string
	Summary     string
	Description string
	Start, End  time.Time
	TimeZone    string
	// AppointmentID queda en extendedProperties para reconciliar con Postgres.
	AppointmentID string
}

// EventID convierte el UUID de una reserva en un id de evento válido. Google exige
// caracteres base32hex (0-9 y a-v) y largo entre 5 y 1024; un UUID sin guiones
// cumple.
func EventID(appointmentID string) string {
	return "agenda" + strings.ReplaceAll(strings.ToLower(appointmentID), "-", "")
}

// CreateEvent crea el evento. Es idempotente: si ya existe, no hace nada.
func (c *Client) CreateEvent(ctx context.Context, calendarID string, e Event) error {
	_, err := c.svc.Events.Insert(calendarID, &gcal.Event{
		Id:          e.ID,
		Summary:     e.Summary,
		Description: e.Description,
		Start:       &gcal.EventDateTime{DateTime: e.Start.Format(time.RFC3339), TimeZone: e.TimeZone},
		End:         &gcal.EventDateTime{DateTime: e.End.Format(time.RFC3339), TimeZone: e.TimeZone},
		ExtendedProperties: &gcal.EventExtendedProperties{
			Private: map[string]string{"appointment_id": e.AppointmentID},
		},
		// Sin recordatorios propios: el calendario del dueño aplica los suyos.
		Reminders: &gcal.EventReminders{UseDefault: true},
	}).Context(ctx).Do()
	if status(err) == http.StatusConflict {
		return nil
	}
	return err
}

// DeleteEvent borra el evento. Es idempotente: si ya no existe, no hace nada.
func (c *Client) DeleteEvent(ctx context.Context, calendarID, eventID string) error {
	err := c.svc.Events.Delete(calendarID, eventID).Context(ctx).Do()
	switch status(err) {
	case http.StatusNotFound, http.StatusGone:
		return nil
	}
	return err
}

func status(err error) int {
	var gerr *googleapi.Error
	if errors.As(err, &gerr) {
		return gerr.Code
	}
	return 0
}
