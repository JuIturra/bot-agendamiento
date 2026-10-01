package booking

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"agendamiento/internal/tenant"
)

// MaxDiasConsulta limita el rango de una consulta de disponibilidad.
const MaxDiasConsulta = 14

var (
	ErrNoEncontrada = errors.New("reserva no encontrada")
	ErrServicio     = errors.New("el servicio no existe")
	ErrRango        = errors.New("fecha_hasta es anterior a fecha_desde")
)

type Reserva struct {
	ID            string
	Servicio      string
	Inicio, Fin   time.Time
	NombreCliente string
}

type NuevaReserva struct {
	TenantID, CustomerID string
	Servicio             string
	Inicio, Fin          time.Time
	NombreCliente        string
}

// Store es lo que booking necesita de la base de datos.
type Store interface {
	// Ocupados devuelve las reservas confirmadas que se cruzan con [desde, hasta).
	Ocupados(ctx context.Context, tenantID string, desde, hasta time.Time) ([]Interval, error)
	// CrearReserva devuelve ErrOcupado si la restricción de no solape la rechaza.
	CrearReserva(ctx context.Context, r NuevaReserva) (Reserva, error)
	ReservasFuturas(ctx context.Context, tenantID, customerID string, desde time.Time) ([]Reserva, error)
	// CancelarReserva devuelve ErrNoEncontrada si la reserva no existe, no es del
	// cliente o ya no está confirmada.
	CancelarReserva(ctx context.Context, tenantID, customerID, id string) (Reserva, error)
}

// Service aplica las reglas de un negocio sobre el Store.
type Service struct {
	Cfg      *tenant.Config
	TenantID string
	Store    Store
	Now      func() time.Time

	// Externos, si no es nil, aporta tramos ocupados que no son reservas del bot:
	// los bloqueos que el dueño pone a mano en Google Calendar.
	Externos func(ctx context.Context, desde, hasta time.Time) ([]Interval, error)
	// AlCambiar, si no es nil, se llama después de crear o cancelar una reserva (p. ej.
	// para sincronizar Calendar de inmediato).
	AlCambiar func()
	Log       *slog.Logger
}

// ocupados junta las reservas de Postgres con los bloqueos externos. Si la fuente
// externa falla, se sigue solo con Postgres: es preferible arriesgar un choque con
// un bloqueo manual a que el bot deje de agendar mientras Google no responde.
func (s *Service) ocupados(ctx context.Context, desde, hasta time.Time) ([]Interval, error) {
	out, err := s.Store.Ocupados(ctx, s.TenantID, desde, hasta)
	if err != nil || s.Externos == nil {
		return out, err
	}
	ext, err := s.Externos(ctx, desde, hasta)
	if err != nil {
		if s.Log != nil {
			s.Log.Error("bloqueos externos no disponibles; se calcula solo con las reservas", "err", err)
		}
		return out, nil
	}
	return append(out, ext...), nil
}

func (s *Service) cambio() {
	if s.AlCambiar != nil {
		s.AlCambiar()
	}
}

// Disponibilidad devuelve slots libres entre dos fechas (inclusive).
func (s *Service) Disponibilidad(ctx context.Context, servicio string, desde, hasta time.Time, franja Franja) ([]time.Time, error) {
	srv, ok := s.Cfg.Servicio(servicio)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrServicio, servicio)
	}
	if hasta.Before(desde) {
		return nil, ErrRango
	}
	if hasta.Sub(desde) > MaxDiasConsulta*24*time.Hour {
		hasta = desde.AddDate(0, 0, MaxDiasConsulta)
	}
	loc := s.Cfg.Location()
	ini := time.Date(desde.Year(), desde.Month(), desde.Day(), 0, 0, 0, 0, loc)
	fin := time.Date(hasta.Year(), hasta.Month(), hasta.Day()+1, 0, 0, 0, 0, loc)
	ocupado, err := s.ocupados(ctx, ini.Add(-s.buffer()), fin.Add(s.buffer()))
	if err != nil {
		return nil, err
	}
	return Slots(s.Cfg, Query{
		Servicio: srv, Desde: desde, Hasta: hasta, Franja: franja,
		Now: s.Now(), Ocupado: ocupado,
	}), nil
}

// Agendar revalida el slot y crea la reserva. Aunque la validación pase, la base de
// datos puede rechazarla si otro cliente reservó ese horario en el mismo instante.
func (s *Service) Agendar(ctx context.Context, customerID, servicio string, inicio time.Time, nombre string) (Reserva, error) {
	srv, ok := s.Cfg.Servicio(servicio)
	if !ok {
		return Reserva{}, fmt.Errorf("%w: %q", ErrServicio, servicio)
	}
	fin := inicio.Add(time.Duration(srv.DuracionMinutos) * time.Minute)
	ocupado, err := s.ocupados(ctx, inicio.Add(-s.buffer()), fin.Add(s.buffer()))
	if err != nil {
		return Reserva{}, err
	}
	if err := Validar(s.Cfg, srv, inicio, s.Now(), ocupado); err != nil {
		return Reserva{}, err
	}
	r, err := s.Store.CrearReserva(ctx, NuevaReserva{
		TenantID: s.TenantID, CustomerID: customerID, Servicio: srv.Nombre,
		Inicio: inicio, Fin: fin, NombreCliente: nombre,
	})
	if err == nil {
		s.cambio()
	}
	return r, err
}

func (s *Service) MisReservas(ctx context.Context, customerID string) ([]Reserva, error) {
	return s.Store.ReservasFuturas(ctx, s.TenantID, customerID, s.Now())
}

func (s *Service) Cancelar(ctx context.Context, customerID, id string) (Reserva, error) {
	r, err := s.Store.CancelarReserva(ctx, s.TenantID, customerID, id)
	if err == nil {
		s.cambio()
	}
	return r, err
}

func (s *Service) buffer() time.Duration {
	return time.Duration(s.Cfg.BufferMinutos) * time.Minute
}
