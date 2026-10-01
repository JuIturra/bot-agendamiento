package store

import (
	"context"

	"github.com/jackc/pgx/v5"

	"agendamiento/internal/notify"
)

// AvisosPendientes implementa notify.Store. Solo considera reservas creadas después
// del registro del dueño, para no mandarle el historial completo al registrarse.
func (s *Store) AvisosPendientes(ctx context.Context, limit int) ([]notify.Pendiente, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT a.id, (t.owner_notify->>'telegram_chat_id')::bigint, t.timezone, a.servicio,
		       COALESCE(a.nombre_cliente, ''), cu.canal, lower(a.periodo),
		       a.estado = 'confirmada', a.aviso_creacion_at IS NOT NULL
		FROM appointments a
		JOIN tenants t ON t.id = a.tenant_id
		JOIN customers cu ON cu.id = a.customer_id
		WHERE t.owner_notify ? 'telegram_chat_id'
		  AND a.created_at >= (t.owner_notify->>'registrado_at')::timestamptz
		  AND a.aviso_proximo_intento <= now()
		  AND ((a.estado = 'confirmada' AND a.aviso_creacion_at IS NULL)
		    OR (a.estado = 'cancelada' AND a.aviso_cancelacion_at IS NULL))
		ORDER BY a.created_at
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (notify.Pendiente, error) {
		var p notify.Pendiente
		err := r.Scan(&p.AppointmentID, &p.ChatID, &p.TimeZone, &p.Servicio, &p.NombreCliente,
			&p.Canal, &p.Inicio, &p.Confirmada, &p.AvisadaCreacion)
		return p, err
	})
}

func (s *Store) MarcarAviso(ctx context.Context, appointmentID string, creacion, cancelacion bool) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE appointments
		SET aviso_creacion_at    = CASE WHEN $2 THEN COALESCE(aviso_creacion_at, now()) ELSE aviso_creacion_at END,
		    aviso_cancelacion_at = CASE WHEN $3 THEN COALESCE(aviso_cancelacion_at, now()) ELSE aviso_cancelacion_at END,
		    aviso_intentos = 0, aviso_error = NULL
		WHERE id = $1`, appointmentID, creacion, cancelacion)
	return err
}

// AvisoError programa el próximo intento: 1, 2, 4... minutos, con un máximo de 1 hora.
func (s *Store) AvisoError(ctx context.Context, appointmentID, msg string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE appointments
		SET aviso_intentos = aviso_intentos + 1,
		    aviso_error = left($2, 1000),
		    aviso_proximo_intento = now() + least(interval '1 minute' * power(2, aviso_intentos), interval '1 hour')
		WHERE id = $1`, appointmentID, msg)
	return err
}

// RegistrarDueno guarda el chat del dueño. Volver a registrarse cambia el chat y la
// fecha de registro (desde ahí se avisa).
func (s *Store) RegistrarDueno(ctx context.Context, tenantID string, chatID int64) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE tenants
		SET owner_notify = jsonb_build_object('telegram_chat_id', $2::bigint, 'registrado_at', now())
		WHERE id = $1`, tenantID, chatID)
	return err
}
