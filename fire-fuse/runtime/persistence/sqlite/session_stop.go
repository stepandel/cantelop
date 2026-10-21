package sqlite

import (
	"encoding/json"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/messagequeue"
)

// FenceSessionStop atomically withdraws the current route and settles its
// transport records. Terminalizing retrying records also prevents a previously
// queued replacement attempt from resurrecting a deliberately stopped owner.
// New messages may activate a new Sandbox after the stop completes.
func (r *MessageDeliveries) FenceSessionStop(sessionID string, sandboxID string, mutation SandboxRecordMutation, now time.Time) error {
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current string
	if err = tx.QueryRow("SELECT sandbox_id FROM sessions WHERE id=?", sessionID).Scan(&current); err != nil {
		return err
	}
	if current != sandboxID {
		return messagequeue.ErrConflict
	}
	stored, err := sandboxRecordTable.get(tx, sandboxID)
	if err != nil {
		return err
	}
	binding, _, err := mutation(stored)
	if err != nil {
		return err
	}
	rows, err := tx.Query("SELECT record FROM message_executions WHERE json_extract(record, '$.session_id')=? AND (terminal=0 OR json_extract(record, '$.phase')='retrying')", sessionID)
	if err != nil {
		return err
	}
	var stopped []messagequeue.Execution
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return err
		}
		var e messagequeue.Execution
		if err = json.Unmarshal(raw, &e); err != nil {
			rows.Close()
			return err
		}
		if e.SessionID == sessionID && (e.Phase == "retrying" || !e.Terminal()) {
			stopped = append(stopped, e)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, e := range stopped {
		m, err := r.get(tx, e.MessageID)
		if err != nil {
			return err
		}
		// Preserve an acknowledged handler outcome and any retained reply.
		status := m.Status
		code := m.ErrorCode
		if status == messagequeue.StatusAccepted || status == messagequeue.StatusHandling {
			status = messagequeue.StatusFailed
			code = "session_stopped"
		}
		_, err = tx.Exec(`UPDATE message_deliveries SET status=?, work_state='unavailable', session_id=?, error_code=NULLIF(?,''), started_at=coalesce(started_at,?), handled_at=coalesce(handled_at,?), updated_at=? WHERE id=?`, status, sessionID, code, formatTime(now), formatTime(now), formatTime(now), e.MessageID)
		if err != nil {
			return err
		}
		m, err = r.get(tx, e.MessageID)
		if err != nil {
			return err
		}
		transition := messagequeue.TransitionUnavailable
		if m.Generation == 0 {
			transition = messagequeue.TransitionFailed
		}
		if err = appendMessageDeliveryEvent(tx, m, transition, now); err != nil {
			return err
		}
		e.Phase, e.ErrorCode = "unavailable", "session_stopped"
		e.PhaseAt, e.UpdatedAt = now, now
		e.Revision++
		raw, err := json.Marshal(e)
		if err != nil {
			return err
		}
		if _, err = tx.Exec("UPDATE message_executions SET record=?,revision=?,terminal=1 WHERE message_id=?", raw, e.Revision, e.MessageID); err != nil {
			return err
		}
	}
	if err = sandboxRecordTable.write(tx, binding, "stop fence"); err != nil {
		return err
	}
	return tx.Commit()
}
