package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"agendamiento/internal/llm/limit"
)

// Reserve implementa limit.Store. Un advisory lock por clave serializa a los procesos
// que usan el mismo modelo, así dos no toman el último cupo a la vez.
func (s *Store) Reserve(ctx context.Context, clave string, desdeDia, now time.Time, decide func(limit.Uso) bool) (int64, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('llm_requests:' || $1))`, clave); err != nil {
		return 0, false, err
	}
	// Limpieza: solo interesan el día en curso y el último minuto.
	if _, err := tx.Exec(ctx, `DELETE FROM llm_requests WHERE clave = $1 AND at < $2`,
		clave, desdeDia.Add(-48*time.Hour)); err != nil {
		return 0, false, err
	}

	var u limit.Uso
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM llm_requests WHERE clave = $1 AND at >= $2`,
		clave, desdeDia).Scan(&u.DiaActual); err != nil {
		return 0, false, err
	}
	rows, err := tx.Query(ctx, `
		SELECT at, tokens_in FROM llm_requests
		WHERE clave = $1 AND at > $2 ORDER BY at`,
		clave, now.Add(-time.Minute))
	if err != nil {
		return 0, false, err
	}
	u.Minuto, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (limit.Request, error) {
		var q limit.Request
		err := r.Scan(&q.At, &q.TokensIn)
		return q, err
	})
	if err != nil {
		return 0, false, err
	}

	if !decide(u) {
		return 0, false, tx.Commit(ctx)
	}
	var id int64
	if err := tx.QueryRow(ctx, `INSERT INTO llm_requests (clave, at) VALUES ($1, $2) RETURNING id`,
		clave, now).Scan(&id); err != nil {
		return 0, false, err
	}
	return id, true, tx.Commit(ctx)
}

func (s *Store) Record(ctx context.Context, id int64, tokensIn int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE llm_requests SET tokens_in = $2 WHERE id = $1`, id, tokensIn)
	return err
}
