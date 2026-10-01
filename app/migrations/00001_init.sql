-- +goose Up
CREATE EXTENSION IF NOT EXISTS btree_gist;

CREATE TABLE tenants (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    slug         text NOT NULL UNIQUE,
    nombre       text NOT NULL,
    timezone     text NOT NULL,
    config       jsonb NOT NULL,
    llm_model    text NOT NULL,
    calendar_id  text,
    owner_notify jsonb,
    activo       boolean NOT NULL DEFAULT true,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE channels (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid NOT NULL REFERENCES tenants(id),
    tipo        text NOT NULL,
    external_id text NOT NULL,
    credentials jsonb,
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tipo, external_id)
);

CREATE TABLE customers (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        uuid NOT NULL REFERENCES tenants(id),
    canal            text NOT NULL,
    external_user_id text NOT NULL,
    nombre           text,
    telefono         text,
    created_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, canal, external_user_id)
);

CREATE TABLE conversations (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       uuid NOT NULL REFERENCES tenants(id),
    customer_id     uuid NOT NULL REFERENCES customers(id),
    estado          text NOT NULL DEFAULT 'bot' CHECK (estado IN ('bot', 'humano', 'cerrada')),
    last_message_at timestamptz NOT NULL DEFAULT now(),
    resumen         text,
    created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX conversations_customer_idx ON conversations (customer_id, last_message_at DESC);

CREATE TABLE messages (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    conversation_id   uuid NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    rol               text NOT NULL CHECK (rol IN ('user', 'assistant', 'tool')),
    contenido         jsonb NOT NULL,
    tokens_in         int NOT NULL DEFAULT 0,
    tokens_out        int NOT NULL DEFAULT 0,
    tokens_cache_read int NOT NULL DEFAULT 0,
    created_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX messages_conversation_idx ON messages (conversation_id, created_at);

CREATE TABLE inbox (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      uuid NOT NULL REFERENCES tenants(id),
    canal          text NOT NULL,
    message_id     text NOT NULL,
    payload        jsonb NOT NULL,
    estado         text NOT NULL DEFAULT 'pendiente',
    procesar_desde timestamptz NOT NULL DEFAULT now(),
    created_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (canal, message_id)
);

CREATE TABLE appointments (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id            uuid NOT NULL REFERENCES tenants(id),
    customer_id          uuid NOT NULL REFERENCES customers(id),
    servicio             text NOT NULL,
    periodo              tstzrange NOT NULL,
    estado               text NOT NULL DEFAULT 'confirmada'
                         CHECK (estado IN ('confirmada', 'cancelada', 'realizada', 'no_asistio')),
    nombre_cliente       text,
    gcal_event_id        text,
    recordatorio_enviado boolean NOT NULL DEFAULT false,
    created_at           timestamptz NOT NULL DEFAULT now(),
    -- Dos reservas confirmadas del mismo negocio no pueden solaparse.
    CONSTRAINT appointments_sin_solape
        EXCLUDE USING gist (tenant_id WITH =, periodo WITH &&) WHERE (estado = 'confirmada')
);
CREATE INDEX appointments_customer_idx ON appointments (customer_id, lower(periodo));

-- +goose Down
DROP TABLE appointments;
DROP TABLE inbox;
DROP TABLE messages;
DROP TABLE conversations;
DROP TABLE customers;
DROP TABLE channels;
DROP TABLE tenants;
