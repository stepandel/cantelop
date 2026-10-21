// Package observability provides the host-side transport for typed lifecycle
// observations. The central observability database remains the source of truth;
// this package only retains batches until they have been acknowledged.
package observability

import (
	"encoding/json"
	"errors"
	"time"
)

const SchemaVersion = 1

type Event struct {
	EventID       string `json:"event_id"`
	SchemaVersion int    `json:"schema_version"`
	Source        string `json:"source"`
	OccurredAt    string `json:"occurred_at"`
	AppID         string `json:"app_id"`
	Type          string `json:"type"`

	WorkspaceID      string `json:"workspace_id,omitempty"`
	LogicalSessionID string `json:"logical_session_id,omitempty"`
	MessageID        string `json:"message_id,omitempty"`
	RequestID        string `json:"request_id,omitempty"`
	AcceptedAt       string `json:"accepted_at,omitempty"`

	AttemptID string `json:"attempt_id,omitempty"`
	Attempt   int    `json:"attempt,omitempty"`
	TraceID   string `json:"trace_id,omitempty"`
	SpanID    string `json:"span_id,omitempty"`
	QueuedAt  string `json:"queued_at,omitempty"`

	SandboxID         string `json:"sandbox_id,omitempty"`
	ConcreteSessionID string `json:"concrete_session_id,omitempty"`
	BuildID           string `json:"build_id,omitempty"`
	AllocatedAt       string `json:"allocated_at,omitempty"`

	Generation  uint64 `json:"generation,omitempty"`
	QuiescentAt string `json:"quiescent_at,omitempty"`
	FinishedAt  string `json:"finished_at,omitempty"`
	ErrorCode   string `json:"error_code,omitempty"`
	Incomplete  bool   `json:"incomplete,omitempty"`

	ParentSpanID string          `json:"parent_span_id,omitempty"`
	Name         string          `json:"name,omitempty"`
	Kind         string          `json:"kind,omitempty"`
	Status       string          `json:"status,omitempty"`
	StartedAt    string          `json:"started_at,omitempty"`
	Attributes   json.RawMessage `json:"attributes,omitempty"`
	LogID        string          `json:"log_id,omitempty"`
	Severity     string          `json:"severity,omitempty"`
	Body         string          `json:"body,omitempty"`
}

type Batch struct {
	Events []Event `json:"events"`
}

type Sink interface {
	Record(...Event) error
}

func NewEvent(eventID, eventType, appID string, occurredAt time.Time) Event {
	return Event{
		EventID: eventID, SchemaVersion: SchemaVersion, Source: "fire_fuse",
		OccurredAt: occurredAt.UTC().Format(time.RFC3339Nano), AppID: appID, Type: eventType,
	}
}

func encodeBatch(events []Event) ([]byte, error) {
	if len(events) < 1 || len(events) > maxBatchEvents {
		return nil, errors.New("observation batch must contain 1-256 events")
	}
	return json.Marshal(Batch{Events: events})
}
