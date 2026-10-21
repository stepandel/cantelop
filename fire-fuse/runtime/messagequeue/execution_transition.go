package messagequeue

import (
	"reflect"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/messageprotocol"
)

// MaxAttempts bounds delivery attempts for one Message, including
// redeliveries after the owning Sandbox is lost before acknowledging intake.
const MaxAttempts = 6

// ExecutionEvidence answers storage questions inside the transaction that
// saves an execution. Transition rules ask only for the evidence they need.
type ExecutionEvidence interface {
	// SessionOnRunningSandbox reports whether the Session is bound to the
	// running Sandbox within the Workspace.
	SessionOnRunningSandbox(sessionID, sandboxID, workspaceID string) (bool, error)
	// SandboxLive reports whether the Sandbox may still be executing work.
	SandboxLive(sandboxID string) (bool, error)
}

// phaseRank orders phases so that a saved execution never moves backwards.
// Terminal phases share the final rank.
func phaseRank(phase messageprotocol.ExecutionPhase) int {
	switch phase {
	case messageprotocol.PhaseAccepted:
		return 1
	case messageprotocol.PhaseRunning:
		return 2
	case messageprotocol.PhaseCancelling:
		return 3
	case messageprotocol.PhaseSettled:
		return 4
	case messageprotocol.PhaseRecovering:
		return 5
	case messageprotocol.PhaseQuiescent, messageprotocol.PhaseUnavailable, messageprotocol.PhaseRetrying:
		return 6
	}
	return 0
}

// Transition validates a proposed save of e against the durable previous
// execution, if any, and its Message. It returns the record to persist with
// PhaseAt and Revision resolved, or the previous record and unchanged=true
// when the save is an idempotent replay. Errors are ErrConflict, ErrInvalid,
// or evidence failures.
func (e Execution) Transition(previous *Execution, message Message, evidence ExecutionEvidence, now time.Time) (next Execution, unchanged bool, err error) {
	switch {
	case previous == nil:
		if e.Revision != 0 || e.Phase != messageprotocol.PhaseSending || message.Status != StatusHandling || message.Attempts != e.Attempt {
			return e, false, ErrConflict
		}
		e.PhaseAt = now
		if err = requireRunningBinding(evidence, e, message); err != nil {
			return e, false, err
		}
	case previous.Phase == messageprotocol.PhaseRetrying:
		old := *previous
		compare := e
		compare.Revision = old.Revision
		compare.PhaseAt = old.PhaseAt
		compare.UpdatedAt = old.UpdatedAt
		if reflect.DeepEqual(compare, old) {
			return old, true, nil
		}
		// Redelivery starts the next attempt on a different Sandbox of the
		// same Session within the original execution deadline.
		if e.Phase != messageprotocol.PhaseSending || e.Revision != 0 || !old.RedeliverySupported || !e.RedeliverySupported || e.Attempt <= old.Attempt || e.SessionID != old.SessionID || e.SandboxID == old.SandboxID || message.Status != StatusHandling || !e.ExecutionDeadline.Equal(old.ExecutionDeadline) {
			return e, false, ErrConflict
		}
		if err = requireRunningBinding(evidence, e, message); err != nil {
			return e, false, err
		}
		e.PhaseAt = now
		e.Revision = old.Revision
	default:
		old := *previous
		if old.Phase == e.Phase {
			e.PhaseAt = old.PhaseAt
		} else {
			e.PhaseAt = now
		}
		compare := e
		compare.Revision = old.Revision
		compare.UpdatedAt = old.UpdatedAt
		if reflect.DeepEqual(compare, old) {
			return old, true, nil
		}
		// Ownership, identity, and deadlines are fixed for an attempt.
		if old.Revision != e.Revision || old.Terminal() || !reflect.DeepEqual(old.Observability, e.Observability) || old.RedeliverySupported != e.RedeliverySupported || old.Attempt != e.Attempt || old.SessionID != e.SessionID || old.SandboxID != e.SandboxID || !old.AdmissionDeadline.Equal(e.AdmissionDeadline) || !old.ExecutionDeadline.Equal(e.ExecutionDeadline) {
			return e, false, ErrConflict
		}
		if old.Receipt != nil && old.Receipt.State.Terminal() && !reflect.DeepEqual(old.Receipt, e.Receipt) {
			return e, false, ErrConflict
		}
		if phaseRank(e.Phase) < phaseRank(old.Phase) {
			return e, false, ErrConflict
		}
		if old.Receipt != nil && (e.Receipt == nil || old.Receipt.AttemptID != e.Receipt.AttemptID || old.Receipt.Generation != e.Receipt.Generation || old.Receipt.Sequence != e.Receipt.Sequence || !old.Receipt.AcceptedAt.Equal(e.Receipt.AcceptedAt) || !old.Receipt.Deadline.Equal(e.Receipt.Deadline)) {
			return e, false, ErrConflict
		}
	}
	if message.Attempts != e.Attempt {
		return e, false, ErrConflict
	}
	if e.Phase == messageprotocol.PhaseRetrying {
		if !e.RedeliverySupported || message.Status != StatusHandling || e.Attempt >= MaxAttempts || !now.Before(e.ExecutionDeadline) || (e.Receipt != nil && e.Receipt.State.Unsuccessful()) {
			return e, false, ErrConflict
		}
		// Redelivery is safe only once the previous Sandbox cannot run it.
		live, err := evidence.SandboxLive(e.SandboxID)
		if err != nil {
			return e, false, err
		}
		if live {
			return e, false, ErrConflict
		}
	}
	if (e.Phase == messageprotocol.PhaseSettled || e.Phase == messageprotocol.PhaseQuiescent) && (e.Receipt == nil || !e.Receipt.State.Terminal()) {
		return e, false, ErrInvalid
	}
	return e, false, nil
}

func requireRunningBinding(evidence ExecutionEvidence, e Execution, message Message) error {
	bound, err := evidence.SessionOnRunningSandbox(e.SessionID, e.SandboxID, message.WorkspaceID)
	if err != nil {
		return err
	}
	if !bound {
		return ErrConflict
	}
	return nil
}

// Settlement maps a settled execution's terminal receipt onto the Message
// result recorded while the Message is still handling.
func (e Execution) Settlement() (Status, string) {
	if e.Receipt.State == messageprotocol.ReceiptSucceeded {
		return StatusHandled, ""
	}
	if e.Receipt.ErrorCode != "" {
		return StatusFailed, e.Receipt.ErrorCode
	}
	if e.Receipt.State == messageprotocol.ReceiptTimedOut {
		return StatusFailed, "message_timed_out"
	}
	return StatusFailed, "message_handler_failed"
}

// DeliveryTransition names the Message delivery event appended when saving
// this execution changes the Message status or work state.
func (e Execution) DeliveryTransition(generation uint64) Transition {
	switch e.Phase {
	case messageprotocol.PhaseRetrying:
		return TransitionRequeued
	case messageprotocol.PhaseQuiescent:
		return TransitionQuiescent
	case messageprotocol.PhaseUnavailable:
		if generation == 0 {
			return TransitionFailed
		}
		return TransitionUnavailable
	}
	return TransitionSettled
}
