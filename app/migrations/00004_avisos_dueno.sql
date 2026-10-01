-- +goose Up
-- Estado de los avisos al dueño de cada reserva, con la misma idea que gcal_*: el
-- estado vive en la reserva, así un aviso no se pierde si el proceso se cae.
--   confirmada sin aviso_creacion_at  → falta avisar la reserva
--   cancelada sin aviso_cancelacion_at → falta avisar la cancelación (si el dueño
--                                        supo de la reserva; si no, se omite)
ALTER TABLE appointments
    ADD COLUMN aviso_creacion_at     timestamptz,
    ADD COLUMN aviso_cancelacion_at  timestamptz,
    ADD COLUMN aviso_intentos        int NOT NULL DEFAULT 0,
    ADD COLUMN aviso_error           text,
    ADD COLUMN aviso_proximo_intento timestamptz NOT NULL DEFAULT now();

CREATE INDEX appointments_aviso_pendiente_idx ON appointments (aviso_proximo_intento)
    WHERE (estado = 'confirmada' AND aviso_creacion_at IS NULL)
       OR (estado = 'cancelada' AND aviso_cancelacion_at IS NULL);

-- +goose Down
DROP INDEX appointments_aviso_pendiente_idx;
ALTER TABLE appointments
    DROP COLUMN aviso_creacion_at,
    DROP COLUMN aviso_cancelacion_at,
    DROP COLUMN aviso_intentos,
    DROP COLUMN aviso_error,
    DROP COLUMN aviso_proximo_intento;
