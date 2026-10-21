package messageprotocol

import (
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"github.com/stepandel/cantelop/internal/resourceid"
)

const SandboxHeader = "X-Cantelop-Sandbox-ID"
const ProtocolHeader = "X-Cantelop-Message-Protocol"
const RuntimePath = "/__cantelop/v2/runtime"

var receiptErrorCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func ValidSandboxID(id string) bool { return resourceid.ValidSandboxID(id) }

// Receipt is acceptance and status evidence for one sandbox-local mailbox entry.
type Receipt struct {
	StartedAt               *time.Time   `json:"started_at,omitempty"`
	SettledAt               *time.Time   `json:"settled_at,omitempty"`
	ErrorCode               string       `json:"error_code,omitempty"`
	AttemptID               string       `json:"attempt_id,omitempty"`
	SandboxID               string       `json:"sandbox_id"`
	MessageID               string       `json:"message_id"`
	Sequence                uint64       `json:"sequence"`
	Generation              uint64       `json:"generation"`
	AcceptedAt              time.Time    `json:"accepted_at"`
	Deadline                time.Time    `json:"deadline"`
	State                   ReceiptState `json:"state"`
	CancellationRequestedAt *time.Time   `json:"cancellation_requested_at,omitempty"`
}

func (r Receipt) Validate(messageID, sandboxID string) error {
	if (r.ErrorCode != "" && !receiptErrorCodePattern.MatchString(r.ErrorCode)) || !ValidSandboxID(sandboxID) || r.SandboxID != sandboxID || r.MessageID != messageID || !ValidMessageID(messageID) ||
		r.Sequence == 0 || r.Sequence > MaxGeneration || r.Generation == 0 || r.Generation > MaxGeneration || r.AcceptedAt.IsZero() || !r.Deadline.After(r.AcceptedAt) || r.Deadline.Sub(r.AcceptedAt) > 7*24*time.Hour {
		return errors.New("invalid mailbox receipt")
	}
	if !r.State.Valid() {
		return errors.New("invalid mailbox status")
	}
	return nil
}

type StreamPosition struct {
	Acknowledged uint64 `json:"acknowledged"`
	Earliest     uint64 `json:"earliest"`
	Latest       uint64 `json:"latest"`
	Dropped      uint64 `json:"dropped,omitempty"`
}
type ActivityStatus struct {
	ID                      string     `json:"id"`
	Deadline                time.Time  `json:"deadline"`
	CancellationRequestedAt *time.Time `json:"cancellation_requested_at,omitempty"`
}
type RuntimeStatus struct {
	SandboxID    string              `json:"sandbox_id"`
	Protocol     int                 `json:"protocol"`
	Generation   uint64              `json:"generation"`
	Quiescent    bool                `json:"quiescent"`
	MessageWork  *ActivityStatus     `json:"message_work"`
	Activity     *ActivityStatus     `json:"activity"`
	Observations StreamPosition      `json:"observations"`
	Events       StreamPosition      `json:"events"`
	Capabilities RuntimeCapabilities `json:"capabilities"`
}

type RuntimeCapabilities struct {
	Steering                bool `json:"steering,omitempty"`
	MessageCancellation     bool `json:"messageCancellation,omitempty"`
	DurableView             bool `json:"durableView,omitempty"`
	Redelivery              bool `json:"redelivery,omitempty"`
	Recovery                bool `json:"recovery"`
	Replies                 bool `json:"replies"`
	CompletionNotifications bool `json:"completionNotifications,omitempty"`
}

type MessageReply struct {
	Reply json.RawMessage `json:"reply"`
}

func (r MessageReply) Validate() error {
	if len(r.Reply) == 0 || len(r.Reply) > 64<<10 || !json.Valid(r.Reply) {
		return errors.New("invalid Message reply")
	}
	return nil
}

type RecoveryReceipt struct {
	RecoveryID string `json:"recovery_id"`
	Generation uint64 `json:"generation"`
	State      string `json:"state"`
}

type RecoveryState struct {
	ID         string `json:"id"`
	SandboxID  string `json:"sandbox_id,omitempty"`
	Generation uint64 `json:"generation,omitempty"`
}

func (r RecoveryReceipt) Validate(recoveryID string) error {
	if !ValidRecoveryID(recoveryID) || r.RecoveryID != recoveryID || r.Generation == 0 || r.Generation > MaxGeneration {
		return errors.New("invalid recovery receipt")
	}
	if r.State != "completed" && r.State != "failed" {
		return errors.New("invalid recovery state")
	}
	return nil
}

// Validate checks the same runtime evidence for local and remote coordinators.
func (r RuntimeStatus) Validate(sandboxID string, now time.Time) error {
	if !ValidSandboxID(sandboxID) || r.SandboxID != sandboxID || r.Protocol != 2 || r.Generation > MaxGeneration {
		return errors.New("invalid runtime identity or generation")
	}
	if r.Activity != nil && (r.Activity.ID == "" || r.Activity.Deadline.IsZero() || r.Activity.Deadline.After(now.Add(24*time.Hour+30*time.Second))) {
		return errors.New("invalid runtime activity")
	}
	if r.MessageWork != nil && (r.MessageWork.Deadline.IsZero() || r.MessageWork.Deadline.After(now.Add(5*time.Minute+30*time.Second))) {
		return errors.New("invalid runtime message work")
	}
	for _, p := range []StreamPosition{r.Events, r.Observations} {
		if p.Acknowledged > p.Latest || p.Latest > MaxGeneration || p.Earliest > p.Latest+1 {
			return errors.New("invalid runtime stream position")
		}
	}
	return nil
}
