// Package messageprotocol defines the private actor Message protocol shared by
// the platform coordinator and the generated SDK Session runtime.
package messageprotocol

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/stepandel/cantelop/internal/resourceid"
)

const (
	MessagePath         = "/__cantelop/v2/messages"
	OutputPath          = "/__cantelop/v2/runtime/events"
	ObservationPath     = "/__cantelop/v2/runtime/observations"
	RecoveryPath        = "/__cantelop/v2/runtime/recoveries"
	MaxGeneration       = uint64(9_007_199_254_740_991)
	MaxKeepAliveSeconds = 604_800
)

var (
	traceParentPattern = regexp.MustCompile(`^00-([0-9a-f]{32})-([0-9a-f]{16})-01$`)
	spanIDPattern      = regexp.MustCompile(`^[0-9a-f]{16}$`)
	logIDPattern       = regexp.MustCompile(`^log_[0-9a-f]{32}$`)
	sessionIDPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	recoveryIDPattern  = regexp.MustCompile(`^rcv_[0-9a-f]{32}$`)
)

type SessionEnvelope struct {
	ID               string `json:"id"`
	WorkspaceID      string `json:"workspace_id"`
	KeepAliveSeconds int    `json:"keep_alive_seconds"`
}

type Message struct {
	ID      string          `json:"id"`
	Payload json.RawMessage `json:"payload"`
}

// ObservabilityContext is minted by the platform for one delivery attempt and
// carried into the Session runtime. Older runtimes may ignore it safely.
type ObservabilityContext struct {
	AttemptID   string `json:"attempt_id"`
	Attempt     int    `json:"attempt"`
	RequestID   string `json:"request_id,omitempty"`
	TraceParent string `json:"traceparent"`
}

func NewObservabilityContext(requestID string, attempt int) (ObservabilityContext, error) {
	if attempt < 1 || attempt > 100 || (requestID != "" && !resourceid.ValidRequestID(requestID)) {
		return ObservabilityContext{}, errors.New("invalid Message observability identity")
	}
	random := make([]byte, 40)
	if _, err := rand.Read(random); err != nil {
		return ObservabilityContext{}, fmt.Errorf("generate Message observability identity: %w", err)
	}
	traceID := fmt.Sprintf("%x", random[16:32])
	if requestID != "" {
		traceID = requestID[len("req_"):]
	}
	return ObservabilityContext{
		AttemptID: "att_" + fmt.Sprintf("%x", random[:16]), Attempt: attempt,
		RequestID: requestID, TraceParent: "00-" + traceID + "-" + fmt.Sprintf("%x", random[32:]) + "-01",
	}, nil
}

func (value ObservabilityContext) Validate() error {
	match := traceParentPattern.FindStringSubmatch(value.TraceParent)
	if !resourceid.ValidAttemptID(value.AttemptID) || value.Attempt < 1 || value.Attempt > 100 ||
		(value.RequestID != "" && !resourceid.ValidRequestID(value.RequestID)) || len(match) != 3 ||
		match[1] == "00000000000000000000000000000000" || match[2] == "0000000000000000" {
		return errors.New("invalid Message observability context")
	}
	if value.RequestID != "" && match[1] != value.RequestID[len("req_"):] {
		return errors.New("Message trace does not match its Request")
	}
	return nil
}

type Request struct {
	Session       SessionEnvelope       `json:"session"`
	Message       Message               `json:"message"`
	Observability *ObservabilityContext `json:"observability,omitempty"`
	Reply         bool                  `json:"reply,omitempty"`
	// AppID is host-authoritative delivery metadata and is never serialized to
	// the customer Session runtime.
	AppID string `json:"-"`
}

type RecoveryRequest struct {
	RecoveryID string          `json:"recovery_id"`
	MessageID  string          `json:"message_id"`
	Session    SessionEnvelope `json:"session"`
}

func (request RecoveryRequest) Validate() error {
	if !ValidRecoveryID(request.RecoveryID) || !ValidMessageID(request.MessageID) || request.Session.Validate() != nil {
		return errors.New("invalid recovery request")
	}
	return nil
}

type OutputEvent struct {
	View      *json.RawMessage `json:"view,omitempty"`
	Cursor    uint64           `json:"cursor"`
	MessageID string           `json:"message_id"`
	Data      json.RawMessage  `json:"event"`
}

type OutputBatch struct {
	Events []OutputEvent `json:"events"`
}

type RuntimeObservation struct {
	Type         string          `json:"type"`
	SpanID       string          `json:"span_id,omitempty"`
	ParentSpanID string          `json:"parent_span_id,omitempty"`
	Name         string          `json:"name,omitempty"`
	Kind         string          `json:"kind,omitempty"`
	Status       string          `json:"status,omitempty"`
	StartedAt    string          `json:"started_at,omitempty"`
	FinishedAt   string          `json:"finished_at,omitempty"`
	Attributes   json.RawMessage `json:"attributes"`
	LogID        string          `json:"log_id,omitempty"`
	Severity     string          `json:"severity,omitempty"`
	Body         string          `json:"body,omitempty"`
	OccurredAt   string          `json:"occurred_at,omitempty"`
}

type ObservationEvent struct {
	Cursor      uint64             `json:"cursor"`
	MessageID   string             `json:"message_id,omitempty"`
	Observation RuntimeObservation `json:"observation"`
}

type ObservationBatch struct {
	Observations []ObservationEvent `json:"observations"`
}

func (batch ObservationBatch) Validate(after uint64, allowEmpty bool) error {
	if (len(batch.Observations) == 0 && !allowEmpty) || len(batch.Observations) > 64 {
		return errors.New("invalid runtime observation batch")
	}
	next := after + 1
	for _, event := range batch.Observations {
		unscopedRuntimeLog := event.MessageID == "" && event.Observation.Type == "log.recorded" &&
			event.Observation.SpanID == ""
		if event.Cursor != next || event.Cursor > MaxGeneration ||
			(!ValidMessageID(event.MessageID) && !unscopedRuntimeLog) || event.Observation.Validate() != nil {
			return errors.New("invalid runtime observation batch")
		}
		next++
	}
	return nil
}

func (value RuntimeObservation) Validate() error {
	if len(value.Attributes) < 2 || len(value.Attributes) > 16<<10 || !json.Valid(value.Attributes) ||
		value.Attributes[0] != '{' {
		return errors.New("invalid runtime observation attributes")
	}
	validTime := func(raw string) bool {
		_, err := time.Parse(time.RFC3339Nano, raw)
		return err == nil
	}
	switch value.Type {
	case "span.started":
		if !validSpanID(value.SpanID) || (value.ParentSpanID != "" && !validSpanID(value.ParentSpanID)) ||
			len(value.Name) < 1 || len(value.Name) > 256 || value.Kind != "internal" || !validTime(value.StartedAt) ||
			value.Status != "" || value.FinishedAt != "" || value.LogID != "" || value.Severity != "" ||
			value.Body != "" || value.OccurredAt != "" {
			return errors.New("invalid runtime span start")
		}
	case "span.completed":
		if !validSpanID(value.SpanID) || (value.Status != "ok" && value.Status != "error") ||
			!validTime(value.FinishedAt) || value.ParentSpanID != "" || value.Name != "" || value.Kind != "" ||
			value.StartedAt != "" || value.LogID != "" || value.Severity != "" || value.Body != "" ||
			value.OccurredAt != "" {
			return errors.New("invalid runtime span completion")
		}
	case "log.recorded":
		if !logIDPattern.MatchString(value.LogID) || (value.SpanID != "" && !validSpanID(value.SpanID)) ||
			(value.Severity != "debug" && value.Severity != "info" && value.Severity != "warn" && value.Severity != "error") ||
			len(value.Body) < 1 || len(value.Body) > 16<<10 || !validTime(value.OccurredAt) || value.ParentSpanID != "" ||
			value.Name != "" || value.Kind != "" || value.Status != "" || value.StartedAt != "" || value.FinishedAt != "" {
			return errors.New("invalid runtime log")
		}
	default:
		return errors.New("unsupported runtime observation")
	}
	return nil
}

func validSpanID(id string) bool {
	return spanIDPattern.MatchString(id) && id != "0000000000000000"
}

func (batch OutputBatch) Validate(after uint64, allowEmpty bool) error {
	if (len(batch.Events) == 0 && !allowEmpty) || len(batch.Events) > 64 {
		return errors.New("invalid runtime output batch")
	}
	next := after + 1
	for _, event := range batch.Events {
		if event.Cursor != next || event.Cursor > MaxGeneration || !ValidMessageID(event.MessageID) ||
			len(event.Data) == 0 || len(event.Data) > 64<<10 || !json.Valid(event.Data) || (event.View != nil && (len(*event.View) == 0 || len(*event.View) > 64<<10 || !json.Valid(*event.View))) {
			return errors.New("invalid runtime output batch")
		}
		next++
	}
	return nil
}

func ValidMessageID(id string) bool  { return resourceid.ValidMessageID(id) }
func ValidRecoveryID(id string) bool { return recoveryIDPattern.MatchString(id) }

func (session SessionEnvelope) Validate() error {
	if !sessionIDPattern.MatchString(session.ID) || !resourceid.ValidWorkspaceID(session.WorkspaceID) ||
		session.KeepAliveSeconds < 0 || session.KeepAliveSeconds > MaxKeepAliveSeconds {
		return errors.New("invalid Session envelope")
	}
	return nil
}

func (request Request) Validate() error {
	if request.Session.Validate() != nil || !ValidMessageID(request.Message.ID) || len(request.Message.Payload) == 0 || len(request.Message.Payload) > 1<<20 ||
		!json.Valid(request.Message.Payload) {
		return errors.New("invalid Message request")
	}
	if request.Observability != nil && request.Observability.Validate() != nil {
		return errors.New("invalid Message request")
	}
	return nil
}

func (request Request) Body() ([]byte, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(request)
}
