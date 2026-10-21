package sqlite

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/messagequeue"
	"github.com/stepandel/cantelop/fire-fuse/runtime/persistence/configcipher"
)

const messageDeliveryCipherScope = "message_delivery"
const messageDeliveryEventRetention = 30 * 24 * time.Hour
const messageReplyRetention = 30 * 24 * time.Hour

type MessageDeliveries struct {
	db           *sql.DB
	cipher       *configcipher.Cipher
	maxAppRows   int64
	maxHostRows  int64
	maxAppBytes  int64
	maxHostBytes int64
}

func NewMessageDeliveries(db *sql.DB, cipher *configcipher.Cipher) *MessageDeliveries {
	return &MessageDeliveries{db: db, cipher: cipher,
		maxAppRows: 20_000, maxHostRows: 100_000,
		maxAppBytes: 128 << 20, maxHostBytes: 1 << 30}
}

func (r *MessageDeliveries) Enqueue(value messagequeue.Message) (messagequeue.Message, bool, error) {
	if value.Status != messagequeue.StatusAccepted || value.WorkState != messagequeue.WorkPending {
		return messagequeue.Message{}, false, messagequeue.ErrInvalid
	}
	if err := value.Validate(); err != nil {
		return messagequeue.Message{}, false, err
	}
	nonce, ciphertext, err := r.cipher.Seal(messageDeliveryCipherScope, value.ID, "payload", string(value.Payload))
	if err != nil {
		return messagequeue.Message{}, false, fmt.Errorf("encrypt Message payload: %w", err)
	}
	tx, err := r.db.Begin()
	if err != nil {
		return messagequeue.Message{}, false, fmt.Errorf("begin Message enqueue: %w", err)
	}
	defer tx.Rollback()
	var appID string
	if err := tx.QueryRow(`SELECT a.id FROM workspaces w JOIN apps a ON a.id = w.app_id
WHERE w.id = ? AND w.archived_at IS NULL AND a.deleted_at IS NULL`, value.WorkspaceID).Scan(&appID); errors.Is(err, sql.ErrNoRows) {
		return messagequeue.Message{}, false, messagequeue.ErrInvalid
	} else if err != nil {
		return messagequeue.Message{}, false, fmt.Errorf("validate Message Workspace: %w", err)
	}
	if existing, err := r.get(tx, value.ID); err == nil {
		if sameMessageEnqueue(existing, value) {
			return existing, false, nil
		}
		return messagequeue.Message{}, false, messagequeue.ErrConflict
	} else if !errors.Is(err, messagequeue.ErrNotFound) {
		return messagequeue.Message{}, false, err
	}
	// Admission bounds in-flight deliveries plus terminal deliveries still
	// inside the reply retention window; reply space is reserved only until
	// a delivery becomes terminal, after which its stored reply is counted.
	const usage = `SELECT count(*), coalesce(sum(length(m.payload_nonce) + length(m.payload_ciphertext) +
CASE WHEN m.work_state IN ('quiescent','unavailable') THEN coalesce(length(m.reply_nonce), 0) + coalesce(length(m.reply_ciphertext), 0)
WHEN m.reply_requested = 1 THEN max(65564, coalesce(length(m.reply_nonce), 0) + coalesce(length(m.reply_ciphertext), 0)) ELSE 0 END), 0)
FROM message_deliveries m `
	const retained = `(m.work_state NOT IN ('quiescent','unavailable') OR m.updated_at >= ?)`
	since := formatTime(value.AcceptedAt.Add(-messageReplyRetention))
	var appRows, appBytes, hostRows, hostBytes int64
	if err := tx.QueryRow(usage+`JOIN workspaces w ON w.id = m.workspace_id WHERE w.app_id = ? AND `+retained, appID, since).Scan(&appRows, &appBytes); err != nil {
		return messagequeue.Message{}, false, fmt.Errorf("inspect App Message storage: %w", err)
	}
	if err := tx.QueryRow(usage+`WHERE `+retained, since).Scan(&hostRows, &hostBytes); err != nil {
		return messagequeue.Message{}, false, fmt.Errorf("inspect host Message storage: %w", err)
	}
	newBytes := int64(len(nonce) + len(ciphertext))
	if value.ReplyRequested {
		newBytes += 65564
	}
	if appRows >= r.maxAppRows || hostRows >= r.maxHostRows ||
		appBytes+newBytes > r.maxAppBytes || hostBytes+newBytes > r.maxHostBytes {
		return messagequeue.Message{}, false, messagequeue.ErrStorageQuota
	}
	_, err = tx.Exec(`INSERT INTO message_deliveries
  (id, workspace_id, logical_session_id, keep_alive_seconds, status, work_state,
   payload_nonce, payload_ciphertext, attempts, accepted_at, updated_at, request_id, reply_requested)
VALUES (?, ?, ?, ?, 'accepted', 'pending', ?, ?, 0, ?, ?, NULLIF(?, ''), ?)`,
		value.ID, value.WorkspaceID, value.LogicalSessionID, value.KeepAliveSeconds,
		nonce, ciphertext, formatTime(value.AcceptedAt), formatTime(value.UpdatedAt), value.RequestID, value.ReplyRequested)
	if err != nil {
		existing, readErr := r.get(tx, value.ID)
		if readErr == nil {
			if sameMessageEnqueue(existing, value) {
				return existing, false, nil
			}
			return messagequeue.Message{}, false, messagequeue.ErrConflict
		}
		return messagequeue.Message{}, false, fmt.Errorf("insert Message delivery: %w", err)
	}
	if err := appendMessageDeliveryEvent(tx, value, messagequeue.TransitionAccepted, value.AcceptedAt); err != nil {
		return messagequeue.Message{}, false, err
	}
	if _, err := tx.Exec(`UPDATE message_deliveries SET reply_nonce=NULL, reply_ciphertext=NULL
WHERE reply_ciphertext IS NOT NULL AND work_state IN ('quiescent','unavailable') AND updated_at < ?`,
		formatTime(value.AcceptedAt.Add(-messageReplyRetention))); err != nil {
		return messagequeue.Message{}, false, fmt.Errorf("prune Message replies: %w", err)
	}
	if _, err := pruneTerminalMessageDeliveryEvents(tx, value.AcceptedAt.Add(-messageDeliveryEventRetention)); err != nil {
		return messagequeue.Message{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return messagequeue.Message{}, false, fmt.Errorf("commit Message enqueue: %w", err)
	}
	return value, true, nil
}

func (r *MessageDeliveries) Get(id string) (messagequeue.Message, error) {
	return r.get(r.db, id)
}

func (r *MessageDeliveries) SaveReply(id string, reply json.RawMessage, at time.Time) (messagequeue.Message, error) {
	if len(reply) == 0 || len(reply) > 64<<10 || !json.Valid(reply) || at.IsZero() {
		return messagequeue.Message{}, messagequeue.ErrInvalid
	}
	nonce, ciphertext, err := r.cipher.Seal(messageDeliveryCipherScope, id, "reply", string(reply))
	if err != nil {
		return messagequeue.Message{}, fmt.Errorf("encrypt Message reply: %w", err)
	}
	result, err := r.db.Exec(`UPDATE message_deliveries SET reply_nonce=?, reply_ciphertext=?, updated_at=? WHERE id=? AND status='handling' AND reply_requested=1 AND reply_ciphertext IS NULL`, nonce, ciphertext, formatTime(at), id)
	if err != nil {
		return messagequeue.Message{}, fmt.Errorf("save Message reply: %w", err)
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		value, readErr := r.Get(id)
		if readErr == nil && string(value.Reply) == string(reply) {
			return value, nil
		}
		return messagequeue.Message{}, messagequeue.ErrConflict
	}
	return r.Get(id)
}

func (r *MessageDeliveries) ClaimNext(at time.Time) (messagequeue.Message, bool, error) {
	if at.IsZero() {
		return messagequeue.Message{}, false, messagequeue.ErrInvalid
	}
	tx, err := r.db.Begin()
	if err != nil {
		return messagequeue.Message{}, false, fmt.Errorf("begin Message claim: %w", err)
	}
	defer tx.Rollback()
	var id string
	// Retry eligibility is derived from durable fields, so workers and restarts
	// share one 1/2/4/8-second backoff capped by the 30-second admission window.
	// Strip UTC's trailing Z when ordering RFC3339Nano values: an exact second
	// must sort before fractional timestamps within that same second.
	err = tx.QueryRow(`SELECT candidate.id
FROM message_deliveries candidate
WHERE candidate.status = 'accepted' AND candidate.work_state = 'pending'
  AND (candidate.attempts = 0 OR julianday(?) >= min(
    julianday(candidate.updated_at) +
      CASE candidate.attempts WHEN 1 THEN 1 WHEN 2 THEN 2 WHEN 3 THEN 4 ELSE 8 END / 86400.0,
    julianday(candidate.accepted_at) + 30.0 / 86400.0
  ))
  AND NOT EXISTS (
    SELECT 1 FROM message_deliveries active
    WHERE active.workspace_id = candidate.workspace_id
      AND active.logical_session_id = candidate.logical_session_id
      AND active.id != candidate.id
      AND (active.status = 'handling' AND active.admitted_at IS NULL OR (active.status = 'accepted' AND
        (rtrim(active.accepted_at, 'Z'), active.id) < (rtrim(candidate.accepted_at, 'Z'), candidate.id)))
  )
ORDER BY rtrim(candidate.accepted_at, 'Z'), candidate.id LIMIT 1`, formatTime(at)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return messagequeue.Message{}, false, nil
	}
	if err != nil {
		return messagequeue.Message{}, false, fmt.Errorf("select Message delivery: %w", err)
	}
	result, err := tx.Exec(`UPDATE message_deliveries
SET status = 'handling', work_state = 'delivering', attempts = attempts + 1,
    started_at = ?, updated_at = ?
WHERE id = ? AND status = 'accepted' AND work_state = 'pending'`, formatTime(at), formatTime(at), id)
	if err != nil {
		return messagequeue.Message{}, false, fmt.Errorf("claim Message delivery: %w", err)
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return messagequeue.Message{}, false, messagequeue.ErrConflict
	}
	value, err := r.get(tx, id)
	if err != nil {
		return messagequeue.Message{}, false, err
	}
	if err := appendMessageDeliveryEvent(tx, value, messagequeue.TransitionHandling, at); err != nil {
		return messagequeue.Message{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return messagequeue.Message{}, false, fmt.Errorf("commit Message claim: %w", err)
	}
	return value, true, nil
}

func (r *MessageDeliveries) RequeueHandling(at time.Time) (int64, error) {
	if at.IsZero() {
		return 0, messagequeue.ErrInvalid
	}
	tx, err := r.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin Message recovery: %w", err)
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT id FROM message_deliveries
WHERE status = 'handling' AND work_state = 'delivering' AND NOT EXISTS (SELECT 1 FROM message_executions e WHERE e.message_id = message_deliveries.id AND e.terminal=0) ORDER BY id`)
	if err != nil {
		return 0, fmt.Errorf("list handling Message deliveries: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, fmt.Errorf("read handling Message delivery: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("close handling Message deliveries: %w", err)
	}
	result, err := tx.Exec(`UPDATE message_deliveries
SET status = 'accepted', work_state = 'pending', admitted_at=NULL, session_id = NULL, generation = NULL,
    error_code = NULL, started_at = NULL, handled_at = NULL, quiescent_at = NULL, updated_at = ?
WHERE status = 'handling' AND work_state = 'delivering' AND NOT EXISTS (SELECT 1 FROM message_executions e WHERE e.message_id = message_deliveries.id AND e.terminal=0)`, formatTime(at))
	if err != nil {
		return 0, fmt.Errorf("requeue handling Message deliveries: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil || count != int64(len(ids)) {
		return 0, fmt.Errorf("requeue handling Message delivery count: %w", errors.Join(err, messagequeue.ErrConflict))
	}
	for _, id := range ids {
		value, err := r.get(tx, id)
		if err != nil {
			return 0, err
		}
		if err := appendMessageDeliveryEvent(tx, value, messagequeue.TransitionRequeued, at); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit Message recovery: %w", err)
	}
	return count, nil
}

func (r *MessageDeliveries) Requeue(id string, at time.Time) (messagequeue.Message, error) {
	if at.IsZero() {
		return messagequeue.Message{}, messagequeue.ErrInvalid
	}
	return r.updateAndAppend(id, messagequeue.TransitionRequeued, at, `UPDATE message_deliveries
SET status = 'accepted', work_state = 'pending', admitted_at=NULL, session_id = NULL, generation = NULL,
    error_code = NULL, started_at = NULL, handled_at = NULL, quiescent_at = NULL, updated_at = ?
WHERE id = ? AND status = 'handling' AND work_state = 'delivering'`, formatTime(at), id)
}

func (r *MessageDeliveries) Fail(
	id, sessionID, errorCode string,
	at time.Time,
) (messagequeue.Message, error) {
	if (sessionID != "" && !messagequeue.ValidConcreteSessionID(sessionID)) ||
		!messagequeue.ValidErrorCode(errorCode) || at.IsZero() {
		return messagequeue.Message{}, messagequeue.ErrInvalid
	}
	return r.updateAndAppend(id, messagequeue.TransitionFailed, at, `UPDATE message_deliveries
SET status = 'failed', work_state = 'unavailable', session_id = NULLIF(?, ''),
    generation = NULL, error_code = ?, handled_at = ?, updated_at = ?
WHERE id = ? AND status = 'handling' AND work_state = 'delivering'`,
		sessionID, errorCode, formatTime(at), formatTime(at), id)
}

func (r *MessageDeliveries) updateAndAppend(
	id string,
	transition messagequeue.Transition,
	at time.Time,
	statement string,
	arguments ...any,
) (messagequeue.Message, error) {
	tx, err := r.db.Begin()
	if err != nil {
		return messagequeue.Message{}, fmt.Errorf("begin Message %s transition: %w", transition, err)
	}
	defer tx.Rollback()
	statement += " AND NOT EXISTS (SELECT 1 FROM message_executions WHERE message_id=message_deliveries.id AND terminal=0)"
	result, err := tx.Exec(statement, arguments...)
	if err != nil {
		return messagequeue.Message{}, fmt.Errorf("apply Message %s transition: %w", transition, err)
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return messagequeue.Message{}, messagequeue.ErrConflict
	}
	value, err := r.get(tx, id)
	if err != nil {
		return messagequeue.Message{}, err
	}
	if err := appendMessageDeliveryEvent(tx, value, transition, at); err != nil {
		return messagequeue.Message{}, err
	}
	if err := tx.Commit(); err != nil {
		return messagequeue.Message{}, fmt.Errorf("commit Message %s transition: %w", transition, err)
	}
	return value, nil
}

func appendMessageDeliveryEvent(
	tx *sql.Tx,
	value messagequeue.Message,
	transition messagequeue.Transition,
	at time.Time,
) error {
	if at.IsZero() || !value.UpdatedAt.Equal(at) {
		return messagequeue.ErrInvalid
	}
	_, err := tx.Exec(`INSERT INTO message_delivery_events
  (message_id, workspace_id, logical_session_id, session_id, request_id, attempt,
   generation, transition, status, work_state, error_code, occurred_at)
VALUES (?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, NULLIF(?, 0), ?, ?, ?, NULLIF(?, ''), ?)`,
		value.ID, value.WorkspaceID, value.LogicalSessionID, value.SessionID, value.RequestID,
		value.Attempts, value.Generation, transition, value.Status, value.WorkState,
		value.ErrorCode, formatTime(at))
	if err != nil {
		return fmt.Errorf("append Message %s event: %w", transition, err)
	}
	return nil
}

func (r *MessageDeliveries) ListEvents(query messagequeue.EventQuery) (messagequeue.EventPage, error) {
	if err := query.Validate(); err != nil {
		return messagequeue.EventPage{}, err
	}
	statement := `SELECT sequence, message_id, workspace_id, logical_session_id,
COALESCE(session_id, ''), COALESCE(request_id, ''), attempt, COALESCE(generation, 0),
transition, status, work_state, COALESCE(error_code, ''), occurred_at,
(SELECT record FROM message_executions WHERE message_executions.message_id = message_delivery_events.message_id)
FROM message_delivery_events`
	var conditions []string
	var arguments []any
	if query.WorkspaceID != "" {
		conditions = append(conditions, "workspace_id = ?")
		arguments = append(arguments, query.WorkspaceID)
	}
	if query.MessageID != "" {
		conditions = append(conditions, "message_id = ?")
		arguments = append(arguments, query.MessageID)
	}
	if query.BeforeSequence > 0 {
		conditions = append(conditions, "sequence < ?")
		arguments = append(arguments, query.BeforeSequence)
	}
	if len(conditions) > 0 {
		statement += " WHERE " + strings.Join(conditions, " AND ")
	}
	statement += " ORDER BY sequence DESC LIMIT ?"
	arguments = append(arguments, query.Limit+1)
	rows, err := r.db.Query(statement, arguments...)
	if err != nil {
		return messagequeue.EventPage{}, fmt.Errorf("list Message delivery events: %w", err)
	}
	defer rows.Close()
	items := make([]messagequeue.Event, 0, query.Limit+1)
	for rows.Next() {
		event, err := scanMessageDeliveryEvent(rows)
		if err != nil {
			return messagequeue.EventPage{}, err
		}
		items = append(items, event)
	}
	if err := rows.Err(); err != nil {
		return messagequeue.EventPage{}, fmt.Errorf("iterate Message delivery events: %w", err)
	}
	page := messagequeue.EventPage{Items: items}
	if len(items) > query.Limit {
		page.Items = items[:query.Limit]
		page.NextBeforeSequence = page.Items[len(page.Items)-1].Sequence
	}
	return page, nil
}

// PruneTerminalEventsBefore removes history only for deliveries whose runtime
// work reached a terminal state before the retention cutoff. Current delivery
// projections, pending work, and in-flight work are never removed.
func (r *MessageDeliveries) PruneTerminalEventsBefore(cutoff time.Time) (int64, error) {
	if cutoff.IsZero() {
		return 0, messagequeue.ErrInvalidEvent
	}
	return pruneTerminalMessageDeliveryEvents(r.db, cutoff)
}

type messageDeliveryEventExecer interface {
	Exec(string, ...any) (sql.Result, error)
}

func pruneTerminalMessageDeliveryEvents(execer messageDeliveryEventExecer, cutoff time.Time) (int64, error) {
	result, err := execer.Exec(`DELETE FROM message_delivery_events
WHERE message_id IN (
  SELECT id FROM message_deliveries
  WHERE work_state IN ('quiescent', 'unavailable') AND updated_at < ?
)`, formatTime(cutoff))
	if err != nil {
		return 0, fmt.Errorf("prune terminal Message delivery events: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("inspect pruned Message delivery events: %w", err)
	}
	return count, nil
}

func scanMessageDeliveryEvent(row scanner) (messagequeue.Event, error) {
	var event messagequeue.Event
	var occurredAt string
	var execution []byte
	if err := row.Scan(
		&event.Sequence, &event.MessageID, &event.WorkspaceID, &event.LogicalSessionID,
		&event.SessionID, &event.RequestID, &event.Attempt, &event.Generation,
		&event.Transition, &event.Status, &event.WorkState, &event.ErrorCode, &occurredAt, &execution,
	); err != nil {
		return messagequeue.Event{}, fmt.Errorf("scan Message delivery event: %w", err)
	}
	parsed, err := parseTime(occurredAt)
	if err != nil {
		return messagequeue.Event{}, err
	}
	if len(execution) > 0 {
		event.CurrentExecution = new(messagequeue.Execution)
		if err := json.Unmarshal(execution, event.CurrentExecution); err != nil {
			return messagequeue.Event{}, err
		}
	}
	event.OccurredAt = parsed
	if event.Sequence < 1 || event.Validate() != nil {
		return messagequeue.Event{}, messagequeue.ErrInvalidEvent
	}
	return event, nil
}

func (r *MessageDeliveries) get(q rowQuerier, id string) (messagequeue.Message, error) {
	var value messagequeue.Message
	var nonce, ciphertext, replyNonce, replyCiphertext []byte
	var sessionID, errorCode, requestID sql.NullString
	var generation sql.NullInt64
	var acceptedAt, updatedAt string
	var startedAt, handledAt, quiescentAt, admittedAt sql.NullString
	err := q.QueryRow(`SELECT id, workspace_id, logical_session_id, keep_alive_seconds, status, work_state,
payload_nonce, payload_ciphertext, session_id, generation, error_code, attempts,
accepted_at, started_at, handled_at, quiescent_at, updated_at, request_id,
reply_requested, reply_nonce, reply_ciphertext, admitted_at
FROM message_deliveries WHERE id = ?`, id).Scan(
		&value.ID, &value.WorkspaceID, &value.LogicalSessionID, &value.KeepAliveSeconds,
		&value.Status, &value.WorkState, &nonce, &ciphertext, &sessionID, &generation,
		&errorCode, &value.Attempts, &acceptedAt, &startedAt, &handledAt, &quiescentAt, &updatedAt, &requestID,
		&value.ReplyRequested, &replyNonce, &replyCiphertext, &admittedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return messagequeue.Message{}, messagequeue.ErrNotFound
	}
	if err != nil {
		return messagequeue.Message{}, fmt.Errorf("read Message delivery: %w", err)
	}
	value.AdmittedAt, err = parseOptionalTime(admittedAt)
	if err != nil {
		return messagequeue.Message{}, err
	}
	payload, err := r.cipher.Open(messageDeliveryCipherScope, value.ID, "payload", nonce, ciphertext)
	if err != nil {
		return messagequeue.Message{}, fmt.Errorf("decrypt Message payload: %w", err)
	}
	value.Payload = json.RawMessage(payload)
	if len(replyCiphertext) > 0 {
		reply, openErr := r.cipher.Open(messageDeliveryCipherScope, value.ID, "reply", replyNonce, replyCiphertext)
		if openErr != nil {
			return messagequeue.Message{}, fmt.Errorf("decrypt Message reply: %w", openErr)
		}
		value.Reply = json.RawMessage(reply)
	}
	value.SessionID, value.ErrorCode, value.RequestID = sessionID.String, errorCode.String, requestID.String
	if generation.Valid {
		value.Generation = uint64(generation.Int64)
	}
	if value.AcceptedAt, err = parseTime(acceptedAt); err != nil {
		return messagequeue.Message{}, err
	}
	if value.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return messagequeue.Message{}, err
	}
	if value.StartedAt, err = parseOptionalTime(startedAt); err != nil {
		return messagequeue.Message{}, err
	}
	if value.HandledAt, err = parseOptionalTime(handledAt); err != nil {
		return messagequeue.Message{}, err
	}
	if value.QuiescentAt, err = parseOptionalTime(quiescentAt); err != nil {
		return messagequeue.Message{}, err
	}
	if err := value.Validate(); err != nil {
		return messagequeue.Message{}, err
	}
	var execution []byte
	err = q.QueryRow("SELECT record FROM message_executions WHERE message_id=?", id).Scan(&execution)
	if err == nil {
		value.Execution = new(messagequeue.Execution)
		if err = json.Unmarshal(execution, value.Execution); err != nil {
			return messagequeue.Message{}, err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return messagequeue.Message{}, err
	}
	return value, nil
}

func parseOptionalTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid {
		return nil, nil
	}
	parsed, err := parseTime(value.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func sameMessageEnqueue(existing, requested messagequeue.Message) bool {
	return existing.ID == requested.ID && existing.WorkspaceID == requested.WorkspaceID &&
		existing.LogicalSessionID == requested.LogicalSessionID &&
		(existing.RequestID == requested.RequestID || (existing.ReplyRequested && requested.ReplyRequested)) &&
		existing.KeepAliveSeconds == requested.KeepAliveSeconds && existing.ReplyRequested == requested.ReplyRequested &&
		string(existing.Payload) == string(requested.Payload)
}
