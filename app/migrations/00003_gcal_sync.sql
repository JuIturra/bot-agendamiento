-- +goose Up
-- Estado de la sincronización de cada reserva con Google Calendar. La regla:
--   confirmada sin gcal_event_id  → hay que crear el evento
--   no confirmada con gcal_event_id → hay que borrarlo
ALTER TABLE appointments
    ADD COLUMN gcal_intentos        int NOT NULL DEFAULT 0,
    ADD COLUMN gcal_error           text,
    ADD COLUMN gcal_proximo_intento timestamptz NOT NULL DEFAULT now();

CREATE INDEX appointments_gcal_pendiente_idx ON appointments (gcal_proximo_intento)
    WHERE (estado = 'confirmada' AND gcal_event_id IS NULL)
       OR (estado <> 'confirmada' AND gcal_event_id IS NOT NULL);

-- +goose Down
DROP INDEX appointments_gcal_pendiente_idx;
ALTER TABLE appointments
    DROP COLUMN gcal_intentos,
    DROP COLUMN gcal_error,
    DROP COLUMN gcal_proximo_intento;
