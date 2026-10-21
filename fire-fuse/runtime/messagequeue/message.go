// Package messagequeue defines platform-owned actor Message delivery records.
// These records are a durable projection of delivery progress; they do not
// mirror the volatile mailbox inside a Sandbox Session runtime.
package messagequeue

import (
	"encoding/json"
	"errors"
	"regexp"
	"time"
)

type Status string
type WorkState string
type Transition string

const (
	StatusAccepted Status = "accepted"
	StatusHandling Status = "handling"
	StatusHandled  Status = "handled"
	StatusFailed   Status = "failed"

	WorkPending            WorkState = "pending"
	WorkDelivering         WorkState = "delivering"
	WorkAwaitingQuiescence WorkState = "awaiting_quiescence"
	WorkQuiescent          WorkState = "quiescent"
	WorkUnavailable        WorkState = "unavailable"

	TransitionAccepted    Transition = "accepted"
	TransitionHandling    Transition = "handling"
	TransitionRequeued    Transition = "requeued"
	TransitionSettled     Transition = "settled"
	TransitionFailed      Transition = "failed"
	TransitionQuiescent   Transition = "quiescent"
	TransitionUnavailable Transition = "unavailable"

	MaxPayloadBytes = 1 << 20
	MaxGeneration   = uint64(9_007_199_254_740_991)
)

func ValidConcreteSessionID(id string) bool { return concreteSessionIDPattern.MatchString(id) }
func ValidErrorCode(code string) bool       { return errorCodePattern.MatchString(code) }

var (
	ErrInvalid      = errors.New("invalid Message delivery")
	ErrNotFound     = errors.New("Message delivery not found")
	ErrConflict     = errors.New("Message delivery conflicts with existing state")
	ErrStorageQuota = errors.New("Message delivery storage quota exceeded")

	messageIDPattern         = regexp.MustCompile(`^msg_[0-9a-f]{32}$`)
	workspaceIDPattern       = regexp.MustCompile(`^wsp_[0-9a-f]{32}$`)
	logicalSessionIDPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	concreteSessionIDPattern = regexp.MustCompile(`^ses_[A-Za-z0-9][A-Za-z0-9_-]{0,59}$`)
	errorCodePattern         = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	requestIDPattern         = regexp.MustCompile(`^req_[0-9a-f]{32}$`)
)

type Message struct {
	AdmittedAt       *time.Time      `json:"admitted_at,omitempty"`
	Execution        *Execution      `json:"execution,omitempty"`
	ID               string          `json:"id"`
	WorkspaceID      string          `json:"workspace_id"`
	LogicalSessionID string          `json:"logical_session_id"`
	RequestID        string          `json:"request_id,omitempty"`
	KeepAliveSeconds int             `json:"keep_alive_seconds"`
	Payload          json.RawMessage `json:"payload,omitempty"`
	ReplyRequested   bool            `json:"reply_requested,omitempty"`
	Reply            json.RawMessage `json:"reply,omitempty"`
	Status           Status          `json:"state"`
	WorkState        WorkState       `json:"work_state"`
	SessionID        string          `json:"session_id,omitempty"`
	Generation       uint64          `json:"generation,omitempty"`
	ErrorCode        string          `json:"error_code,omitempty"`
	Attempts         int             `json:"attempts"`
	AcceptedAt       time.Time       `json:"accepted_at"`
	StartedAt        *time.Time      `json:"started_at,omitempty"`
	HandledAt        *time.Time      `json:"handled_at,omitempty"`
	QuiescentAt      *time.Time      `json:"quiescent_at,omitempty"`
	UpdatedAt        time.Time       `json:"updated_at"`
}

func (message Message) Validate() error {
	if (message.AdmittedAt != nil && (message.AdmittedAt.Before(message.AcceptedAt) || message.Status == StatusAccepted)) || !messageIDPattern.MatchString(message.ID) ||
		!workspaceIDPattern.MatchString(message.WorkspaceID) ||
		!logicalSessionIDPattern.MatchString(message.LogicalSessionID) ||
		(message.RequestID != "" && !requestIDPattern.MatchString(message.RequestID)) ||
		message.KeepAliveSeconds < 0 || message.KeepAliveSeconds > 604800 ||
		len(message.Payload) == 0 || len(message.Payload) > MaxPayloadBytes || !json.Valid(message.Payload) ||
		(len(message.Reply) > 0 && (!message.ReplyRequested || len(message.Reply) > 64<<10 || !json.Valid(message.Reply))) ||
		message.Attempts < 0 || message.AcceptedAt.IsZero() || message.UpdatedAt.Before(message.AcceptedAt) {
		return ErrInvalid
	}
	switch message.Status {
	case StatusAccepted:
		if message.WorkState != WorkPending || message.SessionID != "" ||
			message.Generation != 0 || message.ErrorCode != "" || message.StartedAt != nil ||
			message.HandledAt != nil || message.QuiescentAt != nil {
			return ErrInvalid
		}
	case StatusHandling:
		if message.WorkState != WorkDelivering || message.Attempts < 1 || message.SessionID != "" || message.Generation != 0 ||
			message.ErrorCode != "" || !orderedTime(message.StartedAt, message.AcceptedAt) ||
			message.HandledAt != nil || message.QuiescentAt != nil {
			return ErrInvalid
		}
	case StatusHandled:
		if message.Attempts < 1 || !concreteSessionIDPattern.MatchString(message.SessionID) ||
			message.Generation == 0 || message.ErrorCode != "" ||
			!orderedTime(message.StartedAt, message.AcceptedAt) ||
			!orderedAfter(message.HandledAt, *message.StartedAt) ||
			!validSettledWork(message) {
			return ErrInvalid
		}
	case StatusFailed:
		if message.Attempts < 1 || !errorCodePattern.MatchString(message.ErrorCode) ||
			!orderedTime(message.StartedAt, message.AcceptedAt) ||
			!orderedAfter(message.HandledAt, *message.StartedAt) {
			return ErrInvalid
		}
		if message.Generation == 0 {
			if message.WorkState != WorkUnavailable || message.QuiescentAt != nil ||
				(message.SessionID != "" && !concreteSessionIDPattern.MatchString(message.SessionID)) {
				return ErrInvalid
			}
		} else if !concreteSessionIDPattern.MatchString(message.SessionID) || !validSettledWork(message) {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

func orderedTime(value *time.Time, earliest time.Time) bool {
	return value != nil && !value.Before(earliest)
}

func orderedAfter(value *time.Time, earliest time.Time) bool {
	return value != nil && !value.Before(earliest)
}

func validSettledWork(message Message) bool {
	switch message.WorkState {
	case WorkAwaitingQuiescence:
		return message.QuiescentAt == nil
	case WorkQuiescent:
		return orderedAfter(message.QuiescentAt, *message.HandledAt)
	case WorkUnavailable:
		return message.QuiescentAt == nil
	default:
		return false
	}
}
