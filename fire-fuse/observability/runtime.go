package observability

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/messagedelivery"
)

// RuntimeEvent converts an SDK runtime observation, already bound to its
// authoritative App, Workspace, Message and attempt identity, to an ingest event.
func RuntimeEvent(record messagedelivery.ObservationRecord) (Event, error) {
	observation := record.Observation
	occurredAt := observation.StartedAt
	if occurredAt == "" {
		occurredAt = observation.FinishedAt
	}
	if occurredAt == "" {
		occurredAt = observation.OccurredAt
	}
	at, err := time.Parse(time.RFC3339Nano, occurredAt)
	if err != nil {
		return Event{}, fmt.Errorf("parse runtime observation time: %w", err)
	}
	eventID := observation.SpanID + ":" + observation.Type
	if observation.LogID != "" {
		eventID = observation.LogID
	}
	event := NewEvent(eventID, observation.Type, record.AppID, at)
	event.Source = "sdk"
	event.WorkspaceID = record.WorkspaceID
	event.LogicalSessionID = record.SessionID
	event.SandboxID = record.SandboxID
	event.ConcreteSessionID = record.ConcreteSessionID
	event.MessageID = record.MessageID
	event.AttemptID = record.AttemptID
	event.TraceID = record.TraceID
	event.SpanID = observation.SpanID
	event.ParentSpanID = observation.ParentSpanID
	event.Name = observation.Name
	event.Kind = observation.Kind
	event.Status = observation.Status
	event.StartedAt = observation.StartedAt
	event.FinishedAt = observation.FinishedAt
	event.Attributes = observation.Attributes
	event.LogID = observation.LogID
	event.Severity = observation.Severity
	event.Body = observation.Body
	return event, nil
}

// RuntimeRecorder records SDK runtime observations in sink. It returns nil
// when sink is nil so tracing can be disabled by configuration.
func RuntimeRecorder(sink Sink, logger *slog.Logger) messagedelivery.ObservationRecorder {
	if sink == nil {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	return func(_ context.Context, record messagedelivery.ObservationRecord) error {
		event, err := RuntimeEvent(record)
		if err != nil {
			return err
		}
		if err := sink.Record(event); err != nil {
			logger.Warn("could not spool SDK observation", "event_type", event.Type, "error", err)
		}
		// Observability is deliberately fail-open for user execution. Once the
		// validated record reaches this boundary, acknowledging it prevents a
		// full or unhealthy host spool from blocking the Session handler.
		return nil
	}
}
