package dispatcher

import (
	"context"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/controlclient"
	"github.com/stepandel/cantelop/fire-fuse/messageprotocol"
	"github.com/stepandel/cantelop/fire-fuse/observability"
)

type executionObservationScope struct {
	accepted observability.Event
}

// ensureExecutionObserved atomically spools the Message, attempt, and Sandbox
// before any SDK event can refer to them. The saved attempt identity and stable
// event IDs make reconstruction after restart safe to ingest more than once.
// This cache is only a transport optimization; execution never depends on it.
func (d *ExecutionDispatcher) ensureExecutionObserved(ctx context.Context, e controlclient.MessageExecution) bool {
	if d.lifecycle == nil || e.Observability == nil {
		return false
	}
	trace := e.Observability
	if _, ok := d.traceScopes.Load(trace.AttemptID); ok {
		return true
	}
	if err := trace.Validate(); err != nil {
		d.logger.Warn("Cannot observe execution with invalid trace context", "message_id", e.MessageID, "error", err)
		return false
	}
	message, err := d.control.GetMessageDelivery(ctx, e.MessageID)
	if err != nil {
		d.logger.Warn("Cannot resolve observed Message", "message_id", e.MessageID, "error", err)
		return false
	}
	workspace, err := d.control.GetWorkspace(ctx, message.WorkspaceID)
	if err != nil {
		d.logger.Warn("Cannot resolve observed Workspace", "message_id", e.MessageID, "error", err)
		return false
	}
	accepted := observability.NewEvent(message.ID+":accepted", "message.accepted", workspace.AppID, message.AcceptedAt)
	accepted.WorkspaceID = message.WorkspaceID
	accepted.LogicalSessionID = message.LogicalSessionID
	accepted.MessageID = message.ID
	accepted.RequestID = message.RequestID
	accepted.AcceptedAt = message.AcceptedAt.UTC().Format(time.RFC3339Nano)
	started := accepted
	started.EventID, started.Type = trace.AttemptID+":started", "attempt.started"
	started.AttemptID, started.Attempt = trace.AttemptID, e.Attempt
	started.TraceID, started.SpanID = trace.TraceParent[3:35], trace.TraceParent[36:52]
	started.QueuedAt = accepted.AcceptedAt
	if message.StartedAt != nil {
		started.QueuedAt = message.StartedAt.UTC().Format(time.RFC3339Nano)
	}
	started.OccurredAt = started.QueuedAt
	routed := accepted
	routed.EventID, routed.Type = trace.AttemptID+":routed", "attempt.routed"
	routed.AttemptID = trace.AttemptID
	routed.SandboxID, routed.ConcreteSessionID = e.SandboxID, e.SessionID
	// Unlike UpdatedAt or PhaseAt, the binding time remains stable across
	// reconciliation passes and dispatcher restarts.
	routed.AllocatedAt = bindingStartedAt(e).UTC().Format(time.RFC3339Nano)
	routed.OccurredAt = routed.AllocatedAt
	if err := d.lifecycle.Record(accepted, started, routed); err != nil {
		d.logger.Warn("Could not spool Message lifecycle", "message_id", e.MessageID, "error", err)
		return false
	}
	d.traceScopes.Store(trace.AttemptID, executionObservationScope{accepted: accepted})
	return true
}

func (d *ExecutionDispatcher) observeExecutionFinished(ctx context.Context, e controlclient.MessageExecution) {
	if e.Observability == nil {
		return
	}
	defer d.traceScopes.Delete(e.Observability.AttemptID)
	if !d.ensureExecutionObserved(ctx, e) {
		return
	}
	value, _ := d.traceScopes.Load(e.Observability.AttemptID)
	event := value.(executionObservationScope).accepted
	event.AttemptID = e.Observability.AttemptID
	event.FinishedAt = d.now().UTC().Format(time.RFC3339Nano)
	event.OccurredAt = event.FinishedAt
	if e.Phase == messageprotocol.PhaseQuiescent && e.Receipt != nil && e.Receipt.State == messageprotocol.ReceiptSucceeded {
		event.EventID, event.Type = event.AttemptID+":completed", "attempt.completed"
		event.Generation = e.Receipt.Generation
		event.QuiescentAt = event.FinishedAt
	} else {
		event.EventID, event.Type = event.AttemptID+":failed", "attempt.failed"
		event.ErrorCode = e.ErrorCode
		event.Incomplete = e.Phase == messageprotocol.PhaseUnavailable
		if event.ErrorCode == "" {
			event.ErrorCode = "handler_failed"
			if e.Receipt != nil && e.Receipt.State == messageprotocol.ReceiptTimedOut {
				event.ErrorCode = "message_timed_out"
			}
		}
	}
	if err := d.lifecycle.Record(event); err != nil {
		d.logger.Warn("Could not spool Message completion", "message_id", e.MessageID, "error", err)
	}
}
