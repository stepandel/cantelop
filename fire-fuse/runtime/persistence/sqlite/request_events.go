package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/model"
)

var ErrConflict = errors.New("request event ID already used for different data")

type RequestEvents struct {
	db *sql.DB
}

func NewRequestEvents(db *sql.DB) *RequestEvents { return &RequestEvents{db: db} }

const maxRetainedRequestEvents = 100_000

func (s *RequestEvents) RecordRequestEvent(event model.RequestEvent) (model.RequestEvent, bool, error) {
	if event.Sequence != 0 || event.Validate() != nil {
		return model.RequestEvent{}, false, model.ErrInvalidRequestEvent
	}
	event.ReceivedAt = event.ReceivedAt.UTC()
	event.Duration = time.Duration(event.Duration.Microseconds()) * time.Microsecond
	tx, err := s.db.Begin()
	if err != nil {
		return model.RequestEvent{}, false, fmt.Errorf("begin request event insert: %w", err)
	}
	defer tx.Rollback()
	var workspaceExists int
	if err := tx.QueryRow("SELECT count(*) FROM workspaces WHERE id = ?", event.WorkspaceID).Scan(&workspaceExists); err != nil {
		return model.RequestEvent{}, false, fmt.Errorf("resolve request Workspace: %w", err)
	}
	if workspaceExists == 0 {
		return model.RequestEvent{}, false, model.ErrInvalidRequestEvent
	}
	if event.SessionID != "" {
		var workspaceID string
		if err := tx.QueryRow("SELECT workspace_id FROM sessions WHERE id = ?", event.SessionID).Scan(&workspaceID); err != nil || workspaceID != event.WorkspaceID {
			return model.RequestEvent{}, false, model.ErrInvalidRequestEvent
		}
	}
	result, err := tx.Exec(`
INSERT INTO request_events (
    event_id, workspace_id, session_id, received_at, status_code, duration_us, request_id
)
VALUES (?, ?, NULLIF(?, ''), ?, ?, ?, NULLIF(?, ''))
ON CONFLICT(event_id) DO NOTHING`,
		event.EventID, event.WorkspaceID, event.SessionID,
		event.ReceivedAt.Format(time.RFC3339Nano), event.StatusCode, event.Duration.Microseconds(),
		event.RequestID,
	)
	if err != nil {
		return model.RequestEvent{}, false, fmt.Errorf("insert request event: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return model.RequestEvent{}, false, fmt.Errorf("inspect request event insert: %w", err)
	}
	stored, err := getRequestEvent(tx, event.EventID)
	if err != nil {
		return model.RequestEvent{}, false, err
	}
	if rows == 0 && !sameRequestEvent(stored, event) {
		return model.RequestEvent{}, false, ErrConflict
	}
	if rows == 1 {
		if err := PruneRequestEvents(tx, stored.Sequence, maxRetainedRequestEvents); err != nil {
			return model.RequestEvent{}, false, fmt.Errorf("prune retained request events: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return model.RequestEvent{}, false, fmt.Errorf("commit request event insert: %w", err)
	}
	return stored, rows == 1, nil
}

func PruneRequestEvents(tx *sql.Tx, newestSequence int64, limit int) error {
	_, err := tx.Exec("DELETE FROM request_events WHERE sequence <= ?", newestSequence-int64(limit))
	return err
}

func getRequestEvent(db rowQuerier, eventID string) (model.RequestEvent, error) {
	row := db.QueryRow(`
SELECT sequence, event_id, COALESCE(request_id, ''), workspace_id, COALESCE(session_id, ''), received_at, status_code, duration_us
FROM request_events WHERE event_id = ?`, eventID)
	return scanRequestEvent(row)
}

func sameRequestEvent(left, right model.RequestEvent) bool {
	return left.EventID == right.EventID && left.RequestID == right.RequestID && left.WorkspaceID == right.WorkspaceID &&
		left.SessionID == right.SessionID && left.ReceivedAt.Equal(right.ReceivedAt) &&
		left.StatusCode == right.StatusCode && left.Duration == right.Duration
}

func (s *RequestEvents) ListRequestEvents(query model.RequestEventQuery) (model.RequestEventPage, error) {
	if err := query.Validate(); err != nil {
		return model.RequestEventPage{}, err
	}
	statement := `
SELECT e.sequence, e.event_id, COALESCE(e.request_id, ''), e.workspace_id, COALESCE(e.session_id, ''),
       e.received_at, e.status_code, e.duration_us
FROM request_events e JOIN workspaces env ON env.id = e.workspace_id`
	conditions := make([]string, 0, 2)
	args := make([]any, 0, 3)
	switch {
	case query.WorkspaceID != "":
		conditions = append(conditions, "e.workspace_id = ?")
		args = append(args, query.WorkspaceID)
	case query.AppID != "":
		conditions = append(conditions, "env.app_id = ?")
		args = append(args, query.AppID)
	}
	switch {
	case query.BeforeSequence > 0:
		conditions = append(conditions, "e.sequence < ?")
		args = append(args, query.BeforeSequence)
	case query.AfterSequence > 0:
		conditions = append(conditions, "e.sequence > ?")
		args = append(args, query.AfterSequence)
	}
	if len(conditions) > 0 {
		statement += " WHERE " + strings.Join(conditions, " AND ")
	}
	// Reading forward from a cursor takes the oldest events after it.
	if query.AfterSequence > 0 {
		statement += " ORDER BY e.sequence ASC LIMIT ?"
	} else {
		statement += " ORDER BY e.sequence DESC LIMIT ?"
	}
	args = append(args, query.Limit+1)
	rows, err := s.db.Query(statement, args...)
	if err != nil {
		return model.RequestEventPage{}, fmt.Errorf("list request events: %w", err)
	}
	defer rows.Close()
	items := make([]model.RequestEvent, 0, query.Limit)
	for rows.Next() {
		event, err := scanRequestEvent(rows)
		if err != nil {
			return model.RequestEventPage{}, err
		}
		items = append(items, event)
	}
	if err := rows.Err(); err != nil {
		return model.RequestEventPage{}, fmt.Errorf("iterate request events: %w", err)
	}
	page := model.RequestEventPage{Items: items}
	if len(items) > query.Limit {
		page.Items = items[:query.Limit]
		if query.AfterSequence == 0 {
			page.NextBeforeSequence = page.Items[len(page.Items)-1].Sequence
		}
	}
	if query.AfterSequence > 0 {
		slices.Reverse(page.Items)
	}
	return page, nil
}

func scanRequestEvent(row scanner) (model.RequestEvent, error) {
	var event model.RequestEvent
	var receivedAt string
	var durationUS int64
	if err := row.Scan(
		&event.Sequence, &event.EventID, &event.RequestID, &event.WorkspaceID, &event.SessionID,
		&receivedAt, &event.StatusCode, &durationUS,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.RequestEvent{}, fmt.Errorf("request event disappeared: %w", err)
		}
		return model.RequestEvent{}, fmt.Errorf("scan request event: %w", err)
	}
	parsed, err := time.Parse(time.RFC3339Nano, receivedAt)
	if err != nil || durationUS < 0 || durationUS > int64(^uint64(0)>>1)/int64(time.Microsecond) {
		return model.RequestEvent{}, model.ErrInvalidRequestEvent
	}
	event.ReceivedAt = parsed
	event.Duration = time.Duration(durationUS) * time.Microsecond
	if event.Validate() != nil || event.Sequence < 1 {
		return model.RequestEvent{}, model.ErrInvalidRequestEvent
	}
	return event, nil
}
