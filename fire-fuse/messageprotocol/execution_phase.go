package messageprotocol

// ReceiptState is the sandbox-local mailbox status reported for one Message.
type ReceiptState string

const (
	ReceiptQueued     ReceiptState = "queued"
	ReceiptRunning    ReceiptState = "running"
	ReceiptCancelling ReceiptState = "cancelling"
	ReceiptSucceeded  ReceiptState = "succeeded"
	ReceiptFailed     ReceiptState = "failed"
	ReceiptTimedOut   ReceiptState = "timed_out"
)

// Valid reports whether the runtime may report this mailbox status.
func (s ReceiptState) Valid() bool {
	switch s {
	case ReceiptQueued, ReceiptRunning, ReceiptCancelling, ReceiptSucceeded, ReceiptFailed, ReceiptTimedOut:
		return true
	}
	return false
}

// Terminal reports whether the handler outcome is final and immutable.
func (s ReceiptState) Terminal() bool {
	return s == ReceiptSucceeded || s == ReceiptFailed || s == ReceiptTimedOut
}

// Unsuccessful reports a terminal handler failure, including a timeout.
func (s ReceiptState) Unsuccessful() bool {
	return s == ReceiptFailed || s == ReceiptTimedOut
}

// ExecutionPhase is the coordinator's lifecycle position for one Message
// attempt. It is exposed through Message status and persisted with the
// execution record.
type ExecutionPhase string

const (
	PhaseSending     ExecutionPhase = "sending"
	PhaseAccepted    ExecutionPhase = "accepted"
	PhaseRunning     ExecutionPhase = "running"
	PhaseCancelling  ExecutionPhase = "cancelling"
	PhaseSettled     ExecutionPhase = "settled"
	PhaseRecovering  ExecutionPhase = "recovering"
	PhaseRetrying    ExecutionPhase = "retrying"
	PhaseQuiescent   ExecutionPhase = "quiescent"
	PhaseUnavailable ExecutionPhase = "unavailable"
)

// Valid reports whether the phase belongs to the execution lifecycle.
func (p ExecutionPhase) Valid() bool {
	switch p {
	case PhaseSending, PhaseAccepted, PhaseRunning, PhaseCancelling, PhaseSettled,
		PhaseRecovering, PhaseRetrying, PhaseQuiescent, PhaseUnavailable:
		return true
	}
	return false
}

// Terminal reports whether the coordinator no longer supervises the attempt.
// A retrying attempt is terminal; its redelivery starts a new attempt.
func (p ExecutionPhase) Terminal() bool {
	return p == PhaseQuiescent || p == PhaseUnavailable || p == PhaseRetrying
}
