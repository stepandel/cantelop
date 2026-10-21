package messagequeue

import (
	"github.com/stepandel/cantelop/fire-fuse/messageprotocol"
	"github.com/stepandel/cantelop/fire-fuse/runtime/model"
)

// Execution adds persistence validation to the shared lifecycle projection.
type Execution model.MessageExecution

// RuntimeInspection is the persisted inspection diagnostic contract.
type RuntimeInspection = model.RuntimeInspection

func (e Execution) Validate() error {
	if !messageprotocol.ValidMessageID(e.MessageID) || !messageprotocol.ValidSandboxID(e.SandboxID) || !ValidConcreteSessionID(e.SessionID) || e.Attempt < 1 || e.Revision < 0 || e.AdmissionDeadline.IsZero() || e.ExecutionDeadline.Before(e.AdmissionDeadline) {
		return ErrInvalid
	}
	if e.Observability != nil && (e.Observability.Validate() != nil || e.Observability.Attempt != e.Attempt) {
		return ErrInvalid
	}
	if e.ErrorCode != "" && !ValidErrorCode(e.ErrorCode) {
		return ErrInvalid
	}
	if !e.Phase.Valid() {
		return ErrInvalid
	}
	if e.Receipt != nil && (e.Receipt.Validate(e.MessageID, e.SandboxID) != nil || !e.Receipt.Deadline.Equal(e.ExecutionDeadline)) {
		return ErrInvalid
	}
	if e.Phase != messageprotocol.PhaseSending && e.Phase != messageprotocol.PhaseRecovering && e.Phase != messageprotocol.PhaseUnavailable && e.Phase != messageprotocol.PhaseRetrying && e.Receipt == nil {
		return ErrInvalid
	}
	if e.Recovery != nil && (!messageprotocol.ValidRecoveryID(e.Recovery.ID) ||
		(e.Recovery.SandboxID != "" && !messageprotocol.ValidSandboxID(e.Recovery.SandboxID)) ||
		e.Recovery.Generation > messageprotocol.MaxGeneration) {
		return ErrInvalid
	}
	if e.Phase == messageprotocol.PhaseRecovering && e.Recovery == nil {
		return ErrInvalid
	}
	return nil
}
func (e Execution) Terminal() bool {
	return e.Phase.Terminal()
}
