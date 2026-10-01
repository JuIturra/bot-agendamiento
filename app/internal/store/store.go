// Package store accede a Postgres. Implementa booking.Store y lo que necesita el
// motor de conversación.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"agendamiento/internal/booking"
	"agendamiento/internal/tenant"
	"agendamiento/migrations"
)

type Store struct {
	pool *pgxpool.Pool
}

// Open conecta y aplica las migraciones pendientes.
func Open(ctx context.Context, url string) (*Store, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("conectando a Postgres: %w", err)
	}
	if err := migrate(pool); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migraciones: %w", err)
	}
	return &Store{pool: pool}, nil
}

func migrate(pool *pgxpool.Pool) error {
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	return goose.Up(db, ".")
}

func (s *Store) Close() { s.pool.Close() }

// Pool expone el pool para tests.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// UpsertTenant crea o actualiza el negocio por slug y devuelve su id.
func (s *Store) UpsertTenant(ctx context.Context, c *tenant.Config) (string, error) {
	cfg, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	var id string
	err = s.pool.QueryRow(ctx, `
		INSERT INTO tenants (slug, nombre, timezone, config, llm_model, calendar_id)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''))
		ON CONFLICT (slug) DO UPDATE
		SET nombre = EXCLUDED.nombre, timezone = EXCLUDED.timezone,
		    config = EXCLUDED.config, llm_model = EXCLUDED.llm_model,
		    calendar_id = EXCLUDED.calendar_id, updated_at = now()
		RETURNING id`,
		c.Slug, c.Nombre, c.ZonaHoraria, cfg, c.ModeloLLM, c.GoogleCalendarID,
	).Scan(&id)
	return id, err
}

// TenantID busca un negocio por slug, sin modificarlo.
func (s *Store) TenantID(ctx context.Context, slug string) (string, error) {
	var id string
	err := s.pool.QueryRow(ctx, `SELECT id FROM tenants WHERE slug = $1`, slug).Scan(&id)
	return id, err
}

// UpsertCustomer identifica al cliente por canal e id externo. El nombre se
// actualiza solo si llega uno nuevo.
func (s *Store) UpsertCustomer(ctx context.Context, tenantID, canal, externalID, nombre string) (string, error) {
	var id string
	err := s.pool.QueryRow(ctx, `
		INSERT INTO customers (tenant_id, canal, external_user_id, nombre)
		VALUES ($1, $2, $3, NULLIF($4, ''))
		ON CONFLICT (tenant_id, canal, external_user_id) DO UPDATE
		SET nombre = COALESCE(NULLIF(EXCLUDED.nombre, ''), customers.nombre)
		RETURNING id`,
		tenantID, canal, externalID, nombre,
	).Scan(&id)
	return id, err
}

// Conversation es la conversación vigente de un cliente.
type Conversation struct {
	ID     string
	Estado string
}

// ActiveConversation devuelve la conversación del cliente si tuvo actividad dentro
// de maxIdle; si no, abre una nueva.
func (s *Store) ActiveConversation(ctx context.Context, tenantID, customerID string, maxIdle time.Duration) (Conversation, error) {
	var c Conversation
	err := s.pool.QueryRow(ctx, `
		SELECT id, estado FROM conversations
		WHERE customer_id = $1 AND estado <> 'cerrada' AND last_message_at > now() - make_interval(secs => $2)
		ORDER BY last_message_at DESC LIMIT 1`,
		customerID, maxIdle.Seconds(),
	).Scan(&c.ID, &c.Estado)
	if err == nil {
		return c, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return c, err
	}
	err = s.pool.QueryRow(ctx, `
		INSERT INTO conversations (tenant_id, customer_id) VALUES ($1, $2)
		RETURNING id, estado`,
		tenantID, customerID,
	).Scan(&c.ID, &c.Estado)
	return c, err
}

// Usage son los tokens consumidos para producir un mensaje.
type Usage struct {
	In, Out, CacheRead int64
}

// AddMessage guarda un mensaje y actualiza la actividad de la conversación.
// contenido se serializa a JSON.
func (s *Store) AddMessage(ctx context.Context, conversationID, rol string, contenido any, u Usage) error {
	raw, err := json.Marshal(contenido)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		INSERT INTO messages (conversation_id, rol, contenido, tokens_in, tokens_out, tokens_cache_read)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		conversationID, rol, raw, u.In, u.Out, u.CacheRead,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE conversations SET last_message_at = now() WHERE id = $1`, conversationID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// TextMessage es un turno de texto del historial.
type TextMessage struct {
	Rol  string // "user" | "assistant"
	Text string
}

// RecentTextMessages devuelve los últimos limit turnos de texto, en orden
// cronológico. Los registros de herramientas (rol "tool") no se incluyen.
func (s *Store) RecentTextMessages(ctx context.Context, conversationID string, limit int) ([]TextMessage, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT rol, contenido->>'text' FROM (
			SELECT rol, contenido, created_at FROM messages
			WHERE conversation_id = $1 AND rol IN ('user', 'assistant')
			ORDER BY created_at DESC LIMIT $2
		) m ORDER BY created_at`,
		conversationID, limit,
	)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (TextMessage, error) {
		var m TextMessage
		var text sql.NullString
		err := r.Scan(&m.Rol, &text)
		m.Text = text.String
		return m, err
	})
}

// --- booking.Store ---

func (s *Store) Ocupados(ctx context.Context, tenantID string, desde, hasta time.Time) ([]booking.Interval, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT lower(periodo), upper(periodo) FROM appointments
		WHERE tenant_id = $1 AND estado = 'confirmada' AND periodo && tstzrange($2, $3)
		ORDER BY lower(periodo)`,
		tenantID, desde, hasta,
	)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (booking.Interval, error) {
		var i booking.Interval
		err := r.Scan(&i.Start, &i.End)
		return i, err
	})
}

// sqlstateExclusion es exclusion_violation: la restricción appointments_sin_solape.
const sqlstateExclusion = "23P01"

func (s *Store) CrearReserva(ctx context.Context, r booking.NuevaReserva) (booking.Reserva, error) {
	periodo := pgtype.Range[pgtype.Timestamptz]{
		Lower:     pgtype.Timestamptz{Time: r.Inicio, Valid: true},
		Upper:     pgtype.Timestamptz{Time: r.Fin, Valid: true},
		LowerType: pgtype.Inclusive,
		UpperType: pgtype.Exclusive,
		Valid:     true,
	}
	var id string
	err := s.pool.QueryRow(ctx, `
		INSERT INTO appointments (tenant_id, customer_id, servicio, periodo, nombre_cliente)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''))
		RETURNING id`,
		r.TenantID, r.CustomerID, r.Servicio, periodo, r.NombreCliente,
	).Scan(&id)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == sqlstateExclusion {
		return booking.Reserva{}, booking.ErrOcupado
	}
	if err != nil {
		return booking.Reserva{}, err
	}
	return booking.Reserva{ID: id, Servicio: r.Servicio, Inicio: r.Inicio, Fin: r.Fin, NombreCliente: r.NombreCliente}, nil
}

func (s *Store) ReservasFuturas(ctx context.Context, tenantID, customerID string, desde time.Time) ([]booking.Reserva, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, servicio, lower(periodo), upper(periodo), COALESCE(nombre_cliente, '')
		FROM appointments
		WHERE tenant_id = $1 AND customer_id = $2 AND estado = 'confirmada' AND lower(periodo) >= $3
		ORDER BY lower(periodo)`,
		tenantID, customerID, desde,
	)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanReserva)
}

func (s *Store) CancelarReserva(ctx context.Context, tenantID, customerID, id string) (booking.Reserva, error) {
	rows, err := s.pool.Query(ctx, `
		UPDATE appointments SET estado = 'cancelada'
		WHERE id::text = $1 AND tenant_id = $2 AND customer_id = $3 AND estado = 'confirmada'
		RETURNING id, servicio, lower(periodo), upper(periodo), COALESCE(nombre_cliente, '')`,
		id, tenantID, customerID,
	)
	if err != nil {
		return booking.Reserva{}, err
	}
	r, err := pgx.CollectExactlyOneRow(rows, scanReserva)
	if errors.Is(err, pgx.ErrNoRows) {
		return booking.Reserva{}, booking.ErrNoEncontrada
	}
	return r, err
}

func scanReserva(r pgx.CollectableRow) (booking.Reserva, error) {
	var b booking.Reserva
	err := r.Scan(&b.ID, &b.Servicio, &b.Inicio, &b.Fin, &b.NombreCliente)
	return b, err
}
