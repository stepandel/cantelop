package messagequeue

import (
	"errors"
	"time"
)

const MaxEventPageSize = 500

var ErrInvalidEvent = errors.New("invalid Message delivery event")

// Event is a payload-free durable snapshot appended with a delivery state
// transition. Sequence is assigned by persistence and is the stable cursor.
type Event struct {
	// CurrentExecution is a live projection at read time, not historical event contents.
	CurrentExecution *Execution
	Sequence         int64
	MessageID        string
	WorkspaceID      string
	LogicalSessionID string
	SessionID        string
	RequestID        string
	Attempt          int
	Generation       uint64
	Transition       Transition
	Status           Status
	WorkState        WorkState
	ErrorCode        string
	OccurredAt       time.Time
}

func (event Event) Validate() error {
	if event.Sequence < 0 || !messageIDPattern.MatchString(event.MessageID) ||
		!workspaceIDPattern.MatchString(event.WorkspaceID) ||
		!logicalSessionIDPattern.MatchString(event.LogicalSessionID) || event.Attempt < 0 ||
		(event.SessionID != "" && !concreteSessionIDPattern.MatchString(event.SessionID)) ||
		(event.RequestID != "" && !requestIDPattern.MatchString(event.RequestID)) ||
		(event.Generation > MaxGeneration) ||
		(event.ErrorCode != "" && !errorCodePattern.MatchString(event.ErrorCode)) || event.OccurredAt.IsZero() {
		return ErrInvalidEvent
	}
	hasError := event.ErrorCode != ""
	switch event.Transition {
	case TransitionAccepted:
		if event.Status != StatusAccepted || event.WorkState != WorkPending || event.Attempt != 0 ||
			event.SessionID != "" || event.Generation != 0 || hasError {
			return ErrInvalidEvent
		}
	case TransitionHandling:
		if event.Status != StatusHandling || event.WorkState != WorkDelivering || event.Attempt < 1 ||
			event.SessionID != "" || event.Generation != 0 || hasError {
			return ErrInvalidEvent
		}
	case TransitionRequeued:
		if event.Status != StatusAccepted || event.WorkState != WorkPending || event.Attempt < 1 ||
			event.SessionID != "" || event.Generation != 0 || hasError {
			return ErrInvalidEvent
		}
	case TransitionSettled:
		if !settledEvent(event, WorkAwaitingQuiescence, hasError) {
			return ErrInvalidEvent
		}
	case TransitionFailed:
		if event.Status != StatusFailed || event.WorkState != WorkUnavailable || event.Attempt < 1 ||
			event.Generation != 0 || !hasError {
			return ErrInvalidEvent
		}
	case TransitionQuiescent:
		if !settledEvent(event, WorkQuiescent, hasError) {
			return ErrInvalidEvent
		}
	case TransitionUnavailable:
		if !settledEvent(event, WorkUnavailable, hasError) {
			return ErrInvalidEvent
		}
	default:
		return ErrInvalidEvent
	}
	return nil
}

func settledEvent(event Event, workState WorkState, hasError bool) bool {
	return (event.Status == StatusHandled || event.Status == StatusFailed) && event.WorkState == workState &&
		event.Attempt > 0 && event.SessionID != "" && event.Generation > 0 &&
		((event.Status == StatusFailed) == hasError)
}

type EventPage struct {
	Items              []Event
	NextBeforeSequence int64
}

type EventQuery struct {
	WorkspaceID    string
	MessageID      string
	BeforeSequence int64
	Limit          int
}

func (query EventQuery) Validate() error {
	if query.BeforeSequence < 0 || query.Limit < 1 || query.Limit > MaxEventPageSize ||
		(query.WorkspaceID != "" && !workspaceIDPattern.MatchString(query.WorkspaceID)) ||
		(query.MessageID != "" && !messageIDPattern.MatchString(query.MessageID)) {
		return ErrInvalidEvent
	}
	return nil
}
