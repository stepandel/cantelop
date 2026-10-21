// Package controlclient defines the provider-neutral client boundary used by
// host-local request coordinators to call the Fire Fuse control plane.
package controlclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/stepandel/cantelop/fire-fuse/runtime/messagequeue"
	"net/http"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/session"
)

// ErrAdmissionLimit identifies a control-plane response that can succeed once
// another Session releases App or host capacity.
var ErrAdmissionLimit = errors.New("control-plane admission limit reached")

// Error is one non-success response from the control plane.
type Error struct {
	StatusCode int
	Code       string
}

func (e *Error) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("control plane returned HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("control plane returned HTTP %d (%s)", e.StatusCode, e.Code)
}

func (e *Error) Unwrap() error {
	if e.StatusCode == http.StatusTooManyRequests && e.Code == "admission_limit" {
		return ErrAdmissionLimit
	}
	return nil
}

type App struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
}

type Workspace struct {
	ID         string     `json:"id"`
	AppID      string     `json:"app_id"`
	AppSlug    string     `json:"app_slug,omitempty"`
	Slug       string     `json:"slug"`
	ArchivedAt *time.Time `json:"archived_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at,omitempty"`
	UpdatedAt  time.Time  `json:"updated_at,omitempty"`
}

type Session struct {
	ID               string        `json:"id"`
	WorkspaceID      string        `json:"workspace_id"`
	KeepAliveSeconds int           `json:"keep_alive_seconds"`
	State            session.State `json:"state"`
}

type CreateSessionSpec struct {
	ID               string `json:"id"`
	WorkspaceID      string `json:"workspace_id"`
	KeepAliveSeconds int    `json:"keep_alive_seconds"`
	KeyHash          string `json:"key_hash,omitempty"`
}

type WorkLease struct {
	SessionID string    `json:"session_id"`
	LeaseID   string    `json:"lease_id"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type MessageDelivery struct {
	AdmittedAt       *time.Time              `json:"admitted_at,omitempty"`
	Execution        *messagequeue.Execution `json:"execution,omitempty"`
	ID               string                  `json:"id"`
	WorkspaceID      string                  `json:"workspace_id"`
	LogicalSessionID string                  `json:"logical_session_id"`
	RequestID        string                  `json:"request_id,omitempty"`
	KeepAliveSeconds int                     `json:"keep_alive_seconds"`
	Payload          json.RawMessage         `json:"payload,omitempty"`
	ReplyRequested   bool                    `json:"reply_requested,omitempty"`
	Reply            json.RawMessage         `json:"reply,omitempty"`
	State            string                  `json:"state"`
	WorkState        string                  `json:"work_state"`
	SessionID        string                  `json:"session_id,omitempty"`
	Generation       uint64                  `json:"generation,omitempty"`
	ErrorCode        string                  `json:"error_code,omitempty"`
	Attempts         int                     `json:"attempts"`
	AcceptedAt       time.Time               `json:"accepted_at"`
	StartedAt        *time.Time              `json:"started_at,omitempty"`
	HandledAt        *time.Time              `json:"handled_at,omitempty"`
	QuiescentAt      *time.Time              `json:"quiescent_at,omitempty"`
	UpdatedAt        time.Time               `json:"updated_at"`
}

type MessageDeliveries interface {
	EnqueueMessageDelivery(context.Context, MessageDelivery) (MessageDelivery, error)
	GetMessageDelivery(context.Context, string) (MessageDelivery, error)
	ClaimNextMessageDelivery(context.Context) (*MessageDelivery, error)
	RequeueHandlingMessageDeliveries(context.Context) (int64, error)
	RequeueMessageDelivery(context.Context, string) error
	SaveMessageReply(context.Context, string, json.RawMessage) error
	FailMessageDelivery(context.Context, string, string, string) error
}

type WorkspaceDirectory interface {
	ListWorkspaces(context.Context) ([]Workspace, error)
	ResolveAppWorkspace(context.Context, string, string) (Workspace, error)
	GetWorkspace(context.Context, string) (Workspace, error)
}

type Sessions interface {
	ListSessions(context.Context, string) ([]Session, error)
	GetSession(context.Context, string) (Session, error)
	GetSessionRoute(context.Context, string) (session.Route, error)
	CreateSession(context.Context, CreateSessionSpec) (Session, error)
	TerminateSession(context.Context, string) (Session, error)
	AcquireSessionWorkLease(context.Context, string, string, time.Duration) (WorkLease, error)
	RenewSessionWorkLease(context.Context, string, string, time.Duration) (WorkLease, error)
	ReleaseSessionWorkLease(context.Context, string, string) (Session, error)
}

type ProxyRequestEvent struct {
	EventID     string
	RequestID   string
	WorkspaceID string
	SessionID   string
	ReceivedAt  time.Time
	StatusCode  int
	Duration    time.Duration
}

type RequestEventRecorder interface {
	RecordRequestEvent(context.Context, ProxyRequestEvent) error
}

type SessionEvent struct {
	StreamID        string          `json:"stream_id"`
	SourceSandboxID string          `json:"source_sandbox_id,omitempty"`
	SourceCursor    uint64          `json:"source_cursor,omitempty"`
	Sequence        uint64          `json:"sequence"`
	SessionID       string          `json:"session_id"`
	MessageID       string          `json:"message_id"`
	CreatedAt       time.Time       `json:"created_at"`
	Data            json.RawMessage `json:"data"`
}

type SessionEventAppend struct {
	View            *json.RawMessage `json:"view,omitempty"`
	WorkspaceID     string           `json:"workspace_id"`
	SessionID       string           `json:"session_id"`
	MessageID       string           `json:"message_id"`
	Data            json.RawMessage  `json:"data"`
	SourceSandboxID string           `json:"source_sandbox_id,omitempty"`
	SourceCursor    uint64           `json:"source_cursor,omitempty"`
}

type SessionEventBatch struct {
	StreamID         string         `json:"stream_id"`
	Events           []SessionEvent `json:"events"`
	EarliestSequence uint64         `json:"earliest_sequence,omitempty"`
	LatestSequence   uint64         `json:"latest_sequence,omitempty"`
	Closed           bool           `json:"closed,omitempty"`
}

type SessionEvents interface {
	AppendSessionEvent(context.Context, SessionEventAppend) (SessionEvent, error)
	ReadSessionEvents(context.Context, string, string, uint64, bool) (SessionEventBatch, error)
	CloseSessionEvents(context.Context, string, string) error
}
