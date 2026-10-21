package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/controlclient"
	"github.com/stepandel/cantelop/fire-fuse/messagedelivery"
	"github.com/stepandel/cantelop/fire-fuse/messageprotocol"
	"github.com/stepandel/cantelop/fire-fuse/runtime/session"
)

func TestInspectionErrorSanitization(t *testing.T) {
	for _, tc := range []struct {
		err              error
		category, detail string
	}{
		{context.DeadlineExceeded, "timeout", "inspection deadline exceeded"},
		{&url.Error{Op: "GET", URL: "https://user:secret@runtime/?token=secret", Err: syscall.ECONNREFUSED}, "transport", "connection refused"},
		{&messagedelivery.RuntimeResponseError{StatusCode: 503, Operation: "secret"}, "http", "runtime returned HTTP 503"},
		{fmt.Errorf("secret: %w", messagedelivery.ErrRuntimeProtocol), "protocol", "invalid runtime identity, schema, or protocol response"},
		{errors.New("secret"), "transport", "runtime inspection transport failed (detail redacted)"},
	} {
		category, detail := inspectionError(tc.err)
		if category != tc.category || detail != tc.detail {
			t.Fatalf("%s %s", category, detail)
		}
	}
}

func TestInspectionHistorySurvivesRestartAndRecovery(t *testing.T) {
	d, c, m, e := executionFixture(t)
	group := []controlclient.MessageExecution{e}
	m.inspectErr = syscall.ECONNREFUSED
	first := d.now()
	for i := 0; i < 2; i++ {
		if err := d.reconcile(context.Background(), group); err == nil {
			t.Fatal("missing inspection error")
		}
		// Rebuild from durable state, as after a dispatcher restart.
		group = []controlclient.MessageExecution{c.saved[len(c.saved)-1]}
		d.now = func() time.Time { return first.Add(time.Second) }
	}
	diagnostic := group[0].Inspection
	if diagnostic.ConsecutiveFailures != 2 || !diagnostic.FirstFailureAt.Equal(first) || !diagnostic.LastFailureAt.Equal(first.Add(time.Second)) {
		t.Fatalf("%+v", diagnostic)
	}
	m.inspectErr = nil
	if err := d.reconcile(context.Background(), group); err != nil {
		t.Fatal(err)
	}
	diagnostic = c.saved[len(c.saved)-1].Inspection
	if diagnostic.ConsecutiveFailures != 0 || diagnostic.LastSuccessAt == nil || diagnostic.LastFailureAt == nil {
		t.Fatalf("%+v", diagnostic)
	}
	if m.admissions != 0 || c.released != 0 || len(c.terminated) != 0 {
		t.Fatal("recovery changed ownership")
	}
}

type secondInspectionFailure struct {
	*executionTestMailbox
	calls int
}

func (m *secondInspectionFailure) Inspect(ctx context.Context, route session.Route) (messageprotocol.RuntimeStatus, error) {
	m.calls++
	if m.calls%2 == 0 {
		return messageprotocol.RuntimeStatus{}, context.DeadlineExceeded
	}
	return m.executionTestMailbox.Inspect(ctx, route)
}
func TestSecondInspectionFailureEnforcesActivityDeadline(t *testing.T) {
	d, c, m, e := executionFixture(t)
	e.Phase = "settled"
	m.receipt.State = "succeeded"
	e.Receipt = &m.receipt
	e.PhaseAt = d.now().Add(-time.Minute)
	d.mailbox = &secondInspectionFailure{executionTestMailbox: m}
	if err := d.reconcile(context.Background(), []controlclient.MessageExecution{e}); err != nil {
		t.Fatal(err)
	}
	last := c.saved[len(c.saved)-1]
	if last.ErrorCode != "runtime_unreachable" || last.Inspection.Category != "timeout" || c.released != 1 || len(c.terminated) != 1 || m.admissions != 0 {
		t.Fatalf("%+v", last)
	}
}
func TestInspectionFailureTraceHasSafeEvidenceAndIncompleteOutcome(t *testing.T) {
	d, c, m, sink, e := traceFixture(t)
	m.inspectErr = errors.New("secret URL")
	d.now = func() time.Time { return e.AdmissionDeadline.Add(11 * time.Second) }
	if err := d.reconcile(context.Background(), []controlclient.MessageExecution{e}); err != nil {
		t.Fatal(err)
	}
	var diagnostic, incomplete bool
	for _, batch := range sink.batches {
		for _, event := range batch.Events {
			if event.Type == "log.recorded" {
				diagnostic = true
				if strings.Contains(string(event.Attributes), "secret") || event.AttemptID != e.Observability.AttemptID {
					t.Fatal(event)
				}
			}
			if event.Type == "attempt.failed" {
				incomplete = event.Incomplete && event.ErrorCode == "runtime_unreachable"
			}
		}
	}
	if file := os.Getenv("CANTELOP_TEST_INSPECTION_BATCHES"); file != "" {
		data, err := json.Marshal(sink.batches)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if !diagnostic || !incomplete || c.released != 1 || m.admissions != 0 {
		t.Fatal("missing scoped diagnostic or unsafe cleanup")
	}
}

func TestDispatcherShutdownDoesNotCountAsRuntimeFailure(t *testing.T) {
	d, c, _, e := executionFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := d.inspectRuntime(ctx, session.Route{SandboxID: e.SandboxID}, []controlclient.MessageExecution{e})
	if !errors.Is(err, context.Canceled) || len(c.saved) != 0 || len(c.terminated) != 0 {
		t.Fatal("shutdown changed execution")
	}
}

func TestSettledInspectionCheckpointsKeepFailuresAndDeadlineChangesImmediate(t *testing.T) {
	d, control, mailbox, e := executionFixture(t)
	e.Phase = "settled"
	mailbox.receipt.State = "succeeded"
	e.Receipt = &mailbox.receipt
	runtime := &settledTestMailbox{executionTestMailbox: mailbox}
	d.mailbox = runtime
	group := []controlclient.MessageExecution{e}
	now := d.now()
	d.now = func() time.Time { return now }
	inspect := func(wantWrites int, wantFailure bool) {
		t.Helper()
		before := len(control.saved)
		_, err := d.inspectRuntime(context.Background(), session.Route{SandboxID: e.SandboxID}, group)
		if (err != nil) != wantFailure || len(control.saved)-before != wantWrites {
			t.Fatalf("inspection: writes=%d want=%d err=%v", len(control.saved)-before, wantWrites, err)
		}
	}
	inspect(1, false) // First success is durable, including after restart.
	now = now.Add(settledInspectionCheckpointInterval - time.Second)
	inspect(0, false)
	now = now.Add(time.Second)
	inspect(1, false)

	// A fresh checkpoint never hides a changed supervision deadline.
	runtime.activity = &messageprotocol.ActivityStatus{ID: "claude", Deadline: now.Add(time.Hour)}
	inspect(1, false)
	if group[0].Activity == nil || !group[0].Activity.Deadline.Equal(runtime.activity.Deadline) {
		t.Fatal("activity deadline was not persisted")
	}
	runtime.activity = nil
	inspect(1, false)
	mailbox.outputPosition = messageprotocol.StreamPosition{Acknowledged: 1, Earliest: 2, Latest: 1}
	inspect(0, false) // Output progress alone needs no per-command rewrite.

	mailbox.inspectErr = context.DeadlineExceeded
	inspect(1, true)
	inspect(1, true)
	if group[0].Inspection.ConsecutiveFailures != 2 {
		t.Fatal("failure count was throttled")
	}
	mailbox.inspectErr = nil
	inspect(1, false)
	if group[0].Inspection.ConsecutiveFailures != 0 || group[0].Inspection.LastFailureAt == nil {
		t.Fatal("recovery lost the failure history")
	}
	inspect(0, false)
}

type activityInspectionMailbox struct {
	*executionTestMailbox
	activity  *messageprotocol.ActivityStatus
	cancelled int
}

func (m *activityInspectionMailbox) Inspect(ctx context.Context, route session.Route) (messageprotocol.RuntimeStatus, error) {
	result, err := m.executionTestMailbox.Inspect(ctx, route)
	result.Activity = m.activity
	return result, err
}
func (m *activityInspectionMailbox) CancelActivity(context.Context, session.Route, string) error {
	m.cancelled++
	return nil
}
func TestLongRunningActivityCancellationAndLeaseCleanup(t *testing.T) {
	d, c, m, e := executionFixture(t)
	e.Phase = "settled"
	m.receipt.State = "succeeded"
	e.Receipt = &m.receipt
	e.PhaseAt = d.now()
	deadline := d.now().Add(30 * time.Minute)
	activity := &messageprotocol.ActivityStatus{ID: "activity", Deadline: deadline}
	mailbox := &activityInspectionMailbox{executionTestMailbox: m, activity: activity}
	d.mailbox = mailbox
	e.Activity = activity
	group := []controlclient.MessageExecution{e}
	d.now = func() time.Time { return deadline.Add(time.Second) }
	if err := d.reconcile(context.Background(), group); err != nil {
		t.Fatal(err)
	}
	if mailbox.cancelled != 1 || c.released != 0 || len(c.terminated) != 0 {
		t.Fatal("activity was not given cancellation grace")
	}
	d.now = func() time.Time { return deadline.Add(11 * time.Second) }
	if err := d.reconcile(context.Background(), group); err != nil {
		t.Fatal(err)
	}
	if c.saved[len(c.saved)-1].ErrorCode != "activity_timed_out" || c.released != 1 || len(c.terminated) != 1 || m.admissions != 0 {
		t.Fatal("activity cleanup failed")
	}
}

func TestInspectionFailureCategoriesTerminateWithoutReplay(t *testing.T) {
	for _, cause := range []error{syscall.ECONNRESET, context.DeadlineExceeded, messagedelivery.ErrRuntimeProtocol, &messagedelivery.RuntimeResponseError{StatusCode: 502}} {
		t.Run(cause.Error(), func(t *testing.T) {
			d, c, m, e := executionFixture(t)
			m.inspectErr = cause
			d.now = func() time.Time { return e.AdmissionDeadline.Add(11 * time.Second) }
			if err := d.reconcile(context.Background(), []controlclient.MessageExecution{e}); err != nil {
				t.Fatal(err)
			}
			last := c.saved[len(c.saved)-1]
			category, _ := inspectionError(cause)
			if last.Inspection.Category != category || last.ErrorCode != "runtime_unreachable" || c.released != 1 || m.admissions != 0 || len(c.terminated) != 1 {
				t.Fatalf("%+v", last)
			}
		})
	}
}
