package dispatcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"syscall"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/controlclient"
	"github.com/stepandel/cantelop/fire-fuse/messagedelivery"
	"github.com/stepandel/cantelop/fire-fuse/messageprotocol"
	"github.com/stepandel/cantelop/fire-fuse/observability"
	"github.com/stepandel/cantelop/fire-fuse/runtime/session"
)

// Never serialize err.Error(): transports can include credentials in URLs and
// decoders can quote arbitrary response content. Retain only typed evidence.
func inspectionError(err error) (string, string) {
	var response *messagedelivery.RuntimeResponseError
	var network net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout", "inspection deadline exceeded"
	case errors.As(err, &network) && network.Timeout():
		return "timeout", "transport timeout"
	case errors.As(err, &response):
		return "http", fmt.Sprintf("runtime returned HTTP %d", response.StatusCode)
	case errors.Is(err, messagedelivery.ErrRuntimeProtocol):
		var syntax *json.SyntaxError
		if errors.As(err, &syntax) {
			return "protocol", fmt.Sprintf("invalid JSON at byte %d", syntax.Offset)
		}
		return "protocol", "invalid runtime identity, schema, or protocol response"
	}
	for _, cause := range []error{syscall.ECONNREFUSED, syscall.ECONNRESET, syscall.EPIPE, syscall.ENETUNREACH, syscall.EHOSTUNREACH, io.EOF, io.ErrUnexpectedEOF} {
		if errors.Is(err, cause) {
			return "transport", cause.Error()
		}
	}
	return "transport", "runtime inspection transport failed (detail redacted)"
}

type inspectionPersistenceError struct{ error }

// Successful checks of an unchanged settled execution need only a periodic
// diagnostic checkpoint. Failures, recovery, and ownership deadline changes
// are persisted immediately; runtime inspection itself still runs every pass.
const settledInspectionCheckpointInterval = 30 * time.Second

func (d *ExecutionDispatcher) inspectRuntime(ctx context.Context, route session.Route, group []controlclient.MessageExecution) (messageprotocol.RuntimeStatus, error) {
	snapshot, inspectErr := d.mailbox.Inspect(ctx, route)
	if ctx.Err() != nil {
		return snapshot, ctx.Err()
	}
	now := d.now().UTC()
	// Message work belongs to the current Message. While an unsettled sibling
	// supervises it through its own deadlines, settled history need not copy it.
	unsettled := false
	for _, e := range group {
		unsettled = unsettled || e.Phase != messageprotocol.PhaseSettled
	}
	for i := range group {
		e := group[i]
		diagnostic := controlclient.RuntimeInspection{}
		if e.Inspection != nil {
			diagnostic = *e.Inspection
		}
		recovered := diagnostic.ConsecutiveFailures > 0 && inspectErr == nil
		if inspectErr == nil {
			unchanged := sameActivity(e.Activity, snapshot.Activity) && (unsettled || sameActivity(e.MessageWork, snapshot.MessageWork)) &&
				(e.RecoverySupported || !snapshot.Capabilities.Recovery)
			e.Activity, e.MessageWork = snapshot.Activity, snapshot.MessageWork
			e.RecoverySupported = e.RecoverySupported || snapshot.Capabilities.Recovery
			if e.Phase == messageprotocol.PhaseSettled && unchanged && !recovered && diagnostic.LastSuccessAt != nil &&
				!now.Before(*diagnostic.LastSuccessAt) && now.Sub(*diagnostic.LastSuccessAt) < settledInspectionCheckpointInterval {
				continue
			}
		}
		if inspectErr != nil {
			if diagnostic.ConsecutiveFailures == 0 {
				diagnostic.FirstFailureAt = &now
			}
			diagnostic.LastFailureAt = &now
			diagnostic.ConsecutiveFailures++
			diagnostic.Category, diagnostic.Error = inspectionError(inspectErr)
		} else {
			diagnostic.LastSuccessAt = &now
			diagnostic.ConsecutiveFailures = 0
		}
		e.Inspection = &diagnostic
		saved, err := d.control.SaveExecution(ctx, e)
		if err != nil {
			return snapshot, inspectionPersistenceError{err}
		}
		group[i] = saved
		if inspectErr != nil || recovered {
			d.observeInspection(ctx, saved, recovered)
		}
	}
	return snapshot, inspectErr
}

// Compare instants, not time.Time encodings that differ across JSON round trips.
func sameActivity(a, b *messageprotocol.ActivityStatus) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.ID != b.ID || !a.Deadline.Equal(b.Deadline) {
		return false
	}
	if a.CancellationRequestedAt == nil || b.CancellationRequestedAt == nil {
		return a.CancellationRequestedAt == b.CancellationRequestedAt
	}
	return a.CancellationRequestedAt.Equal(*b.CancellationRequestedAt)
}

func (d *ExecutionDispatcher) inspectionFailed(ctx context.Context, group []controlclient.MessageExecution, cause error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// Persistence failures must not be mistaken for failed runtime inspections.
	var persistence inspectionPersistenceError
	if errors.As(cause, &persistence) {
		return cause
	}
	for _, e := range group {
		if e.Inspection == nil || e.Inspection.ConsecutiveFailures == 0 {
			return cause
		}
	}
	deadline := supervisionDeadline(group[0])
	for _, e := range group[1:] {
		if at := supervisionDeadline(e); at.Before(deadline) {
			deadline = at
		}
	}
	if d.now().After(deadline.Add(supervisionGrace)) {
		// Inspection alone cannot establish process death or OOM.
		return d.terminate(ctx, group, "runtime_unreachable")
	}
	category, detail := inspectionError(cause)
	return fmt.Errorf("runtime inspection %s: %s", category, detail)
}

func (d *ExecutionDispatcher) observeInspection(ctx context.Context, e controlclient.MessageExecution, recovered bool) {
	if !d.ensureExecutionObserved(ctx, e) {
		return
	}
	value, _ := d.traceScopes.Load(e.Observability.AttemptID)
	scope := value.(executionObservationScope).accepted
	id := fmt.Sprintf("%s:inspection:%d", e.Observability.AttemptID, e.Revision)
	event := observability.NewEvent(id, "log.recorded", scope.AppID, d.now())
	sum := sha256.Sum256([]byte(id))
	event.LogID = "log_" + hex.EncodeToString(sum[:16])
	event.AttemptID, event.TraceID, event.SpanID = e.Observability.AttemptID, e.Observability.TraceParent[3:35], e.Observability.TraceParent[36:52]
	event.Severity, event.Body = "warn", "Runtime inspection failed; workload outcome unknown"
	if recovered {
		event.Severity, event.Body = "info", "Runtime inspection recovered"
	}
	event.Attributes, _ = json.Marshal(e.Inspection)
	if err := d.lifecycle.Record(event); err != nil {
		d.logger.Warn("Could not spool runtime inspection", "message_id", e.MessageID, "error", err)
	}
}
