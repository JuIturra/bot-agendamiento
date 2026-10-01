package calendar

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"agendamiento/internal/bg"
)

// Pendiente es una reserva cuyo evento en Calendar hay que crear o borrar.
type Pendiente struct {
	AppointmentID string
	CalendarID    string
	TimeZone      string
	Servicio      string
	NombreCliente string
	Canal         string
	Inicio, Fin   time.Time
	// Confirmada indica qué hacer: true = crear el evento, false = borrarlo.
	Confirmada bool
	EventID    string // solo para borrar
}

// SyncStore es lo que el sincronizador necesita de la base.
type SyncStore interface {
	// PendientesCalendar devuelve reservas confirmadas sin evento y reservas
	// canceladas que todavía tienen evento, cuyo próximo intento ya venció.
	PendientesCalendar(ctx context.Context, limit int) ([]Pendiente, error)
	// CalendarSincronizado guarda el id del evento creado, o lo limpia (eventID "")
	// si se borró.
	CalendarSincronizado(ctx context.Context, appointmentID, eventID string) error
	// CalendarError registra el fallo y programa el próximo intento con backoff.
	CalendarError(ctx context.Context, appointmentID, msg string) error
}

type EventWriter interface {
	CreateEvent(ctx context.Context, calendarID string, e Event) error
	DeleteEvent(ctx context.Context, calendarID, eventID string) error
}

// Syncer lleva a Calendar los cambios de las reservas. Corre en segundo plano:
// una reserva queda confirmada en Postgres aunque Google esté caído, y el evento se
// crea cuando vuelva.
type Syncer struct {
	Calendar EventWriter
	Store    SyncStore
	Log      *slog.Logger
	bg.Loop
}

// Run sincroniza hasta que ctx se cancela. Kick (de bg.Loop) pide un barrido
// inmediato, p. ej. justo después de una reserva, para que el evento aparezca en
// segundos.
func (s *Syncer) Run(ctx context.Context) {
	s.Loop.Run(ctx, "sync calendar", s.Log, func(ctx context.Context) error {
		_, err := s.SyncOnce(ctx)
		return err
	})
}

// SyncOnce procesa las pendientes y devuelve cuántas quedaron sincronizadas. Un
// error de Google en una reserva no corta el barrido: se registra y se reintenta
// más tarde.
func (s *Syncer) SyncOnce(ctx context.Context) (int, error) {
	pendientes, err := s.Store.PendientesCalendar(ctx, 50)
	if err != nil {
		return 0, err
	}
	ok := 0
	for _, p := range pendientes {
		eventID, err := s.aplicar(ctx, p)
		if err != nil {
			s.Log.Warn("sync calendar: se reintentará", "appointment", p.AppointmentID, "err", err)
			if err := s.Store.CalendarError(ctx, p.AppointmentID, err.Error()); err != nil {
				return ok, err
			}
			continue
		}
		if err := s.Store.CalendarSincronizado(ctx, p.AppointmentID, eventID); err != nil {
			return ok, err
		}
		accion := "evento creado"
		if !p.Confirmada {
			accion = "evento borrado"
		}
		s.Log.Info("sync calendar: "+accion, "appointment", p.AppointmentID)
		ok++
	}
	return ok, nil
}

func (s *Syncer) aplicar(ctx context.Context, p Pendiente) (eventID string, err error) {
	if !p.Confirmada {
		return "", s.Calendar.DeleteEvent(ctx, p.CalendarID, p.EventID)
	}
	e := Event{
		ID:            EventID(p.AppointmentID),
		Summary:       fmt.Sprintf("%s — %s", p.Servicio, p.NombreCliente),
		Description:   fmt.Sprintf("Reservado por el asistente (canal: %s).\nId de reserva: %s", p.Canal, p.AppointmentID),
		Start:         p.Inicio,
		End:           p.Fin,
		TimeZone:      p.TimeZone,
		AppointmentID: p.AppointmentID,
	}
	return e.ID, s.Calendar.CreateEvent(ctx, p.CalendarID, e)
}
