-- +goose Up
-- Registro de requests a proveedores de LLM, para respetar límites de cuota
-- (RPM, TPM, RPD) entre procesos y reinicios.
CREATE TABLE llm_requests (
    id        bigserial PRIMARY KEY,
    clave     text NOT NULL,         -- "proveedor/modelo": los límites son por modelo
    at        timestamptz NOT NULL DEFAULT now(),
    tokens_in int NOT NULL DEFAULT 0 -- se completa al terminar la llamada
);
CREATE INDEX llm_requests_clave_at_idx ON llm_requests (clave, at);

-- +goose Down
DROP TABLE llm_requests;
