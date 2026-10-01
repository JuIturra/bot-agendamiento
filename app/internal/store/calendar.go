package store

import (
	"context"

	"github.com/jackc/pgx/v5"

	"agendamiento/internal/calendar"
)

// PendientesCalendar implementa calendar.SyncStore. Solo considera negocios con
// calendario configurado, y no crea eventos de reservas que ya terminaron.
func (s *Store) PendientesCalendar(ctx context.Context, limit int) ([]calendar.Pendiente, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT a.id, t.calendar_id, t.timezone, a.servicio, COALESCE(a.nombre_cliente, ''),
		       cu.canal, lower(a.periodo), upper(a.periodo), a.estado = 'confirmada',
		       COALESCE(a.gcal_event_id, '')
		FROM appointments a
		JOIN tenants t ON t.id = a.tenant_id
		JOIN customers cu ON cu.id = a.customer_id
		WHERE t.calendar_id IS NOT NULL
		  AND a.gcal_proximo_intento <= now()
		  AND ((a.estado = 'confirmada' AND a.gcal_event_id IS NULL AND upper(a.periodo) > now())
		    OR (a.estado <> 'confirmada' AND a.gcal_event_id IS NOT NULL))
		ORDER BY a.gcal_proximo_intento
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (calendar.Pendiente, error) {
		var p calendar.Pendiente
		err := r.Scan(&p.AppointmentID, &p.CalendarID, &p.TimeZone, &p.Servicio, &p.NombreCliente,
			&p.Canal, &p.Inicio, &p.Fin, &p.Confirmada, &p.EventID)
		return p, err
	})
}

func (s *Store) CalendarSincronizado(ctx context.Context, appointmentID, eventID string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE appointments
		SET gcal_event_id = NULLIF($2, ''), gcal_intentos = 0, gcal_error = NULL
		WHERE id = $1`, appointmentID, eventID)
	return err
}

// CalendarError programa el próximo intento con backoff exponencial: 1, 2, 4... minutos,
// con un máximo de 1 hora.
func (s *Store) CalendarError(ctx context.Context, appointmentID, msg string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE appointments
		SET gcal_intentos = gcal_intentos + 1,
		    gcal_error = left($2, 1000),
		    gcal_proximo_intento = now() + least(interval '1 minute' * power(2, gcal_intentos), interval '1 hour')
		WHERE id = $1`, appointmentID, msg)
	return err
}
