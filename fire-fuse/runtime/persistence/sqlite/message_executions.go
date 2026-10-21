package sqlite

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/messageprotocol"
	"github.com/stepandel/cantelop/fire-fuse/runtime/messagequeue"
)

func (r *MessageDeliveries) ListExecutions() ([]messagequeue.Execution, error) {
	rows, err := r.db.Query("SELECT record FROM message_executions WHERE terminal = 0 ORDER BY message_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []messagequeue.Execution{}
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		var e messagequeue.Execution
		if err = json.Unmarshal(raw, &e); err != nil {
			return nil, err
		}
		result = append(result, e)
	}
	return result, rows.Err()
}

// executionEvidence answers transition rule queries inside the save transaction.
type executionEvidence struct{ tx *sql.Tx }

func (q executionEvidence) SessionOnRunningSandbox(sessionID, sandboxID, workspaceID string) (bool, error) {
	var binding int
	if err := q.tx.QueryRow("SELECT count(*) FROM sessions s JOIN sandboxes b ON b.id=s.sandbox_id WHERE s.id=? AND s.sandbox_id=? AND s.workspace_id=? AND b.state='running'", sessionID, sandboxID, workspaceID).Scan(&binding); err != nil {
		return false, fmt.Errorf("inspect execution Session binding: %w", err)
	}
	return binding == 1, nil
}

func (q executionEvidence) SandboxLive(sandboxID string) (bool, error) {
	var live int
	if err := q.tx.QueryRow("SELECT count(*) FROM sandboxes WHERE id=? AND state IN ('starting','running','stopping')", sandboxID).Scan(&live); err != nil {
		return false, fmt.Errorf("inspect execution Sandbox: %w", err)
	}
	return live != 0, nil
}

// SaveExecution reads the durable execution and its Message, applies the
// messagequeue transition rules, and writes the result together with its
// Message delivery projection in one transaction.
func (r *MessageDeliveries) SaveExecution(e messagequeue.Execution, now time.Time) (messagequeue.Execution, error) {
	if e.Validate() != nil {
		return e, messagequeue.ErrInvalid
	}
	tx, err := r.db.Begin()
	if err != nil {
		return e, fmt.Errorf("begin execution save: %w", err)
	}
	defer tx.Rollback()
	message, err := r.get(tx, e.MessageID)
	if err != nil {
		return e, err
	}
	var previous *messagequeue.Execution
	var raw []byte
	err = tx.QueryRow("SELECT record FROM message_executions WHERE message_id=?", e.MessageID).Scan(&raw)
	switch {
	case err == nil:
		previous = new(messagequeue.Execution)
		if err = json.Unmarshal(raw, previous); err != nil {
			return e, fmt.Errorf("decode stored execution: %w", err)
		}
	case !errors.Is(err, sql.ErrNoRows):
		return e, fmt.Errorf("read stored execution: %w", err)
	}
	e, unchanged, err := e.Transition(previous, message, executionEvidence{tx}, now)
	if err != nil || unchanged {
		return e, err
	}
	if err = projectExecution(tx, e, message, now); err != nil {
		return e, err
	}
	updated, err := r.get(tx, e.MessageID)
	if err != nil {
		return e, err
	}
	if updated.Status != message.Status || updated.WorkState != message.WorkState {
		if err = appendMessageDeliveryEvent(tx, updated, e.DeliveryTransition(updated.Generation), now); err != nil {
			return e, err
		}
	}
	e.Revision++
	e.UpdatedAt = now
	raw, err = json.Marshal(e)
	if err != nil {
		return e, fmt.Errorf("encode execution: %w", err)
	}
	_, err = tx.Exec(`INSERT INTO message_executions(message_id,record,revision,terminal) VALUES(?,?,?,?) ON CONFLICT(message_id) DO UPDATE SET record=excluded.record,revision=excluded.revision,terminal=excluded.terminal`, e.MessageID, raw, e.Revision, e.Terminal())
	if err != nil {
		return e, fmt.Errorf("write execution: %w", err)
	}
	return e, tx.Commit()
}

// projectExecution writes a validated execution's effect onto its Message
// delivery row.
func projectExecution(tx *sql.Tx, e messagequeue.Execution, message messagequeue.Message, now time.Time) error {
	at := formatTime(now)
	if e.Phase == messageprotocol.PhaseRetrying {
		if _, err := tx.Exec(`UPDATE message_deliveries SET status='accepted',work_state='pending',admitted_at=NULL,session_id=NULL,generation=NULL,error_code=NULL,started_at=NULL,handled_at=NULL,quiescent_at=NULL,updated_at=? WHERE id=?`, at, e.MessageID); err != nil {
			return fmt.Errorf("requeue Message for redelivery: %w", err)
		}
	}
	if e.Receipt != nil {
		if _, err := tx.Exec(`UPDATE message_deliveries SET admitted_at=coalesce(admitted_at,?) WHERE id=?`, at, e.MessageID); err != nil {
			return fmt.Errorf("record Message admission: %w", err)
		}
	}
	if (e.Phase == messageprotocol.PhaseSettled || e.Phase == messageprotocol.PhaseQuiescent) && message.Status == messagequeue.StatusHandling {
		status, code := e.Settlement()
		if _, err := tx.Exec(`UPDATE message_deliveries SET status=?,work_state='awaiting_quiescence',session_id=?,generation=?,error_code=NULLIF(?,''),handled_at=?,updated_at=? WHERE id=?`, string(status), e.SessionID, e.Receipt.Generation, code, at, at, e.MessageID); err != nil {
			return fmt.Errorf("record Message settlement: %w", err)
		}
	}
	if e.Phase == messageprotocol.PhaseQuiescent {
		if _, err := tx.Exec("UPDATE message_deliveries SET work_state='quiescent',quiescent_at=?,updated_at=? WHERE id=?", at, at, e.MessageID); err != nil {
			return fmt.Errorf("record Message quiescence: %w", err)
		}
	}
	if e.Phase == messageprotocol.PhaseUnavailable {
		var err error
		if message.Status == messagequeue.StatusHandling {
			_, err = tx.Exec(`UPDATE message_deliveries SET status='failed',work_state='unavailable',session_id=?,error_code=?,handled_at=?,updated_at=? WHERE id=?`, e.SessionID, "execution_outcome_unknown", at, at, e.MessageID)
		} else {
			_, err = tx.Exec("UPDATE message_deliveries SET work_state='unavailable',updated_at=? WHERE id=?", at, e.MessageID)
		}
		if err != nil {
			return fmt.Errorf("record Message unavailability: %w", err)
		}
	}
	return nil
}
