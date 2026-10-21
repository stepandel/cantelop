package model

import (
	"github.com/stepandel/cantelop/fire-fuse/messageprotocol"
	"time"
)

// MessageExecution stores ownership and lifecycle evidence, never a persistent event stream.
// RuntimeInspection retains the latest failure and the current failure streak.
// Error is a bounded, allowlisted diagnostic; it never contains runtime bodies or URLs.
type RuntimeInspection struct {
	Category            string     `json:"category,omitempty"`
	Error               string     `json:"error,omitempty"`
	FirstFailureAt      *time.Time `json:"first_failure_at,omitempty"`
	LastFailureAt       *time.Time `json:"last_failure_at,omitempty"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	LastSuccessAt       *time.Time `json:"last_success_at,omitempty"`
}

type MessageExecution struct {
	RedeliverySupported bool                                  `json:"redelivery_supported,omitempty"`
	Inspection          *RuntimeInspection                    `json:"inspection,omitempty"`
	PhaseAt             time.Time                             `json:"phase_at"`
	Activity            *messageprotocol.ActivityStatus       `json:"activity,omitempty"`
	MessageWork         *messageprotocol.ActivityStatus       `json:"message_work,omitempty"`
	MessageID           string                                `json:"message_id"`
	Attempt             int                                   `json:"attempt"`
	SessionID           string                                `json:"session_id"`
	SandboxID           string                                `json:"sandbox_id"`
	Revision            int64                                 `json:"revision"`
	Phase               messageprotocol.ExecutionPhase        `json:"phase"`
	ErrorCode           string                                `json:"error_code,omitempty"`
	AdmissionDeadline   time.Time                             `json:"admission_deadline"`
	ExecutionDeadline   time.Time                             `json:"execution_deadline"`
	Receipt             *messageprotocol.Receipt              `json:"receipt,omitempty"`
	UpdatedAt           time.Time                             `json:"updated_at"`
	Observability       *messageprotocol.ObservabilityContext `json:"observability,omitempty"`
	RecoverySupported   bool                                  `json:"recovery_supported,omitempty"`
	Recovery            *messageprotocol.RecoveryState        `json:"recovery,omitempty"`
}
