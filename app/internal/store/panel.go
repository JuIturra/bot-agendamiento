package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"agendamiento/internal/panel"
)

// Condiciones compartidas con los sincronizadores (store/calendar.go, store/avisos.go):
// si cambian allá, tienen que cambiar acá.
const (
	sqlCalendarPendiente = `((a.estado = 'confirmada' AND a.gcal_event_id IS NULL AND upper(a.periodo) > now())
		OR (a.estado <> 'confirmada' AND a.gcal_event_id IS NOT NULL))`
	sqlAvisoPendiente = `a.created_at >= (t.owner_notify->>'registrado_at')::timestamptz
		AND ((a.estado = 'confirmada' AND a.aviso_creacion_at IS NULL)
		  OR (a.estado = 'cancelada' AND a.aviso_cancelacion_at IS NULL))`
	// Una conversación "terminó en reserva" si alguna llamada a agendar respondió OK.
	sqlAgendoOK = `m.rol = 'tool' AND m.contenido->>'name' = 'agendar' AND m.contenido->>'is_error' = 'false'`
)

// Resumen implementa panel.Datos.
func (s *Store) Resumen(ctx context.Context, tenantID string, desde, inicioDiaLLM time.Time, claveLLM string) (panel.Resumen, error) {
	var r panel.Resumen
	var ultimo *time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM conversations WHERE tenant_id = t.id AND created_at >= $2),
		  (SELECT count(DISTINCT m.conversation_id) FROM messages m JOIN conversations c ON c.id = m.conversation_id
		     WHERE c.tenant_id = t.id AND c.created_at >= $2 AND `+sqlAgendoOK+`),
		  (SELECT count(*) FROM messages m JOIN conversations c ON c.id = m.conversation_id
		     WHERE c.tenant_id = t.id AND m.rol = 'user' AND m.created_at >= $2),
		  (SELECT count(*) FROM appointments WHERE tenant_id = t.id AND created_at >= $2),
		  (SELECT count(*) FROM appointments WHERE tenant_id = t.id AND created_at >= $2 AND estado = 'cancelada'),
		  (SELECT count(*) FROM appointments WHERE tenant_id = t.id AND estado = 'confirmada' AND lower(periodo) >= now()),
		  (SELECT COALESCE(sum(m.tokens_in), 0) FROM messages m JOIN conversations c ON c.id = m.conversation_id
		     WHERE c.tenant_id = t.id AND m.created_at >= $2),
		  (SELECT COALESCE(sum(m.tokens_out), 0) FROM messages m JOIN conversations c ON c.id = m.conversation_id
		     WHERE c.tenant_id = t.id AND m.created_at >= $2),
		  (SELECT max(m.created_at) FROM messages m JOIN conversations c ON c.id = m.conversation_id
		     WHERE c.tenant_id = t.id AND m.rol = 'user'),
		  (SELECT count(*) FROM llm_requests WHERE clave = $4 AND at >= $3),

		  t.calendar_id IS NOT NULL,
		  (SELECT count(*) FROM appointments a WHERE a.tenant_id = t.id AND `+sqlCalendarPendiente+`),
		  (SELECT count(*) FROM appointments a WHERE a.tenant_id = t.id AND a.gcal_error IS NOT NULL AND `+sqlCalendarPendiente+`),
		  COALESCE((SELECT a.gcal_error FROM appointments a WHERE a.tenant_id = t.id AND a.gcal_error IS NOT NULL
		     AND `+sqlCalendarPendiente+` ORDER BY a.gcal_proximo_intento DESC LIMIT 1), ''),

		  COALESCE(t.owner_notify ? 'telegram_chat_id', false),
		  (SELECT count(*) FROM appointments a WHERE a.tenant_id = t.id AND `+sqlAvisoPendiente+`),
		  (SELECT count(*) FROM appointments a WHERE a.tenant_id = t.id AND a.aviso_error IS NOT NULL AND `+sqlAvisoPendiente+`),
		  COALESCE((SELECT a.aviso_error FROM appointments a WHERE a.tenant_id = t.id AND a.aviso_error IS NOT NULL
		     AND `+sqlAvisoPendiente+` ORDER BY a.aviso_proximo_intento DESC LIMIT 1), '')
		FROM tenants t WHERE t.id = $1`,
		tenantID, desde, inicioDiaLLM, claveLLM,
	).Scan(&r.Conversaciones, &r.ConversacionesConReserva, &r.MensajesCliente, &r.Reservas, &r.Cancelaciones,
		&r.Proximas, &r.TokensIn, &r.TokensOut, &ultimo, &r.LLMHoy,
		&r.CalendarActivo, &r.CalendarPendientes, &r.CalendarConError, &r.CalendarUltimoError,
		&r.DuenoRegistrado, &r.AvisosPendientes, &r.AvisosConError, &r.AvisoUltimoError)
	if ultimo != nil {
		r.UltimoMensaje = *ultimo
	}
	return r, err
}

const sqlConversacionFila = `
	SELECT c.id, COALESCE(cu.nombre, ''), cu.canal, c.estado, c.created_at, c.last_message_at,
	       count(m.id) FILTER (WHERE m.rol = 'user'),
	       COALESCE(sum(m.tokens_in), 0), COALESCE(sum(m.tokens_out), 0),
	       COALESCE(bool_or(` + sqlAgendoOK + `), false),
	       COALESCE((SELECT contenido->>'text' FROM messages
	                 WHERE conversation_id = c.id AND rol = 'user' ORDER BY created_at DESC LIMIT 1), '')
	FROM conversations c
	JOIN customers cu ON cu.id = c.customer_id
	LEFT JOIN messages m ON m.conversation_id = c.id
	WHERE c.tenant_id = $1 %s
	GROUP BY c.id, cu.nombre, cu.canal
	ORDER BY c.last_message_at DESC
	LIMIT $2`

func scanConversacionFila(r pgx.CollectableRow) (panel.ConversacionFila, error) {
	var f panel.ConversacionFila
	var ultimoTexto string
	err := r.Scan(&f.ID, &f.Cliente, &f.Canal, &f.Estado, &f.Inicio, &f.Ultimo,
		&f.MensajesCliente, &f.TokensIn, &f.TokensOut, &f.Agendo, &ultimoTexto)
	f.Vista = vistaPrevia(ultimoTexto, 90)
	return f, err
}

func (s *Store) Conversaciones(ctx context.Context, tenantID string, limit int) ([]panel.ConversacionFila, error) {
	rows, err := s.pool.Query(ctx, strings.Replace(sqlConversacionFila, "%s", "", 1), tenantID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanConversacionFila)
}

func (s *Store) Conversacion(ctx context.Context, tenantID, id string) (panel.Conversacion, error) {
	var c panel.Conversacion
	// id::text: un id mal formado en la URL es "no encontrada", no un error de tipo.
	rows, err := s.pool.Query(ctx, strings.Replace(sqlConversacionFila, "%s", "AND c.id::text = $3", 1), tenantID, 1, id)
	if err != nil {
		return c, err
	}
	fila, err := pgx.CollectExactlyOneRow(rows, scanConversacionFila)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, panel.ErrNoEncontrada
	}
	if err != nil {
		return c, err
	}
	c.ConversacionFila = fila

	rows, err = s.pool.Query(ctx, `
		SELECT rol, created_at, contenido, tokens_in, tokens_out, tokens_cache_read
		FROM messages WHERE conversation_id = $1 ORDER BY created_at, id`, fila.ID)
	if err != nil {
		return c, err
	}
	c.Mensajes, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (panel.Mensaje, error) {
		var m panel.Mensaje
		var raw []byte
		if err := r.Scan(&m.Rol, &m.At, &raw, &m.TokensIn, &m.TokensOu, &m.Cache); err != nil {
			return m, err
		}
		var cont struct {
			Text    string          `json:"text"`
			Name    string          `json:"name"`
			Input   json.RawMessage `json:"input"`
			Result  string          `json:"result"`
			IsError bool            `json:"is_error"`
		}
		if err := json.Unmarshal(raw, &cont); err != nil {
			m.Texto = string(raw)
			return m, nil
		}
		m.Texto, m.Tool, m.Result, m.IsError = cont.Text, cont.Name, cont.Result, cont.IsError
		m.Input = string(cont.Input)
		return m, nil
	})
	return c, err
}

func (s *Store) Reservas(ctx context.Context, tenantID string, desde time.Time, limit int) ([]panel.ReservaFila, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT a.id, a.servicio, COALESCE(a.nombre_cliente, ''), cu.canal, a.estado,
		       lower(a.periodo), a.created_at, a.gcal_event_id IS NOT NULL, COALESCE(a.gcal_error, ''),
		       a.aviso_creacion_at IS NOT NULL, a.aviso_cancelacion_at IS NOT NULL, COALESCE(a.aviso_error, '')
		FROM appointments a JOIN customers cu ON cu.id = a.customer_id
		WHERE a.tenant_id = $1 AND upper(a.periodo) >= $2
		ORDER BY lower(a.periodo) DESC
		LIMIT $3`, tenantID, desde, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (panel.ReservaFila, error) {
		var f panel.ReservaFila
		err := r.Scan(&f.ID, &f.Servicio, &f.Cliente, &f.Canal, &f.Estado, &f.Inicio, &f.Creada,
			&f.EnCalendar, &f.CalendarError, &f.Avisada, &f.AvisadaCancel, &f.AvisoError)
		return f, err
	})
}

// vistaPrevia quita el prefijo de fecha que el motor antepone a cada mensaje del
// cliente ("[martes 30-09-2026 a las 14:32]\n") y corta el texto.
func vistaPrevia(texto string, max int) string {
	if strings.HasPrefix(texto, "[") {
		if _, resto, ok := strings.Cut(texto, "]\n"); ok {
			texto = resto
		}
	}
	texto = strings.Join(strings.Fields(texto), " ")
	if r := []rune(texto); len(r) > max {
		return string(r[:max]) + "…"
	}
	return texto
}
