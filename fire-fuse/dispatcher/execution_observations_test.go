package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/controlclient"
	"github.com/stepandel/cantelop/fire-fuse/messagedelivery"
	"github.com/stepandel/cantelop/fire-fuse/messageprotocol"
	"github.com/stepandel/cantelop/fire-fuse/observability"
	"github.com/stepandel/cantelop/fire-fuse/runtime/session"
)

type traceTestControl struct {
	*executionTestControl
	message controlclient.MessageDelivery
}

func (c *traceTestControl) GetMessageDelivery(context.Context, string) (controlclient.MessageDelivery, error) {
	return c.message, nil
}
func (c *traceTestControl) GetWorkspace(context.Context, string) (controlclient.Workspace, error) {
	return controlclient.Workspace{AppID: "app_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, nil
}

type traceTestMailbox struct {
	*executionTestMailbox
	events []messageprotocol.ObservationEvent
}

func (m *traceTestMailbox) CollectObservations(_ context.Context, _ session.Route, observe func(messageprotocol.ObservationEvent) error) error {
	for _, e := range m.events {
		if err := observe(e); err != nil {
			return err
		}
	}
	return nil
}

type traceTestSink struct {
	batches []observability.Batch
	err     error
}

func (s *traceTestSink) Record(events ...observability.Event) error {
	if s.err != nil {
		return s.err
	}
	s.batches = append(s.batches, observability.Batch{Events: events})
	return nil
}

func traceFixture(t *testing.T) (*ExecutionDispatcher, *executionTestControl, *traceTestMailbox, *traceTestSink, controlclient.MessageExecution) {
	d, c, m, e := executionFixture(t)
	trace, err := messageprotocol.NewObservabilityContext("req_44444444444444444444444444444444", 1)
	if err != nil {
		t.Fatal(err)
	}
	e.Observability = &trace
	m.receipt.AttemptID = trace.AttemptID
	m.receipt.State = "succeeded"
	m.quiescent = true
	now := d.now()
	d.control = &traceTestControl{executionTestControl: c, message: controlclient.MessageDelivery{
		ID: e.MessageID, RequestID: trace.RequestID, WorkspaceID: "wsp_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		LogicalSessionID: "slack:test", AcceptedAt: now.Add(-time.Second), StartedAt: &now,
	}}
	sink := &traceTestSink{}
	d.lifecycle = sink
	mailbox := &traceTestMailbox{executionTestMailbox: m}
	d.mailbox = mailbox
	return d, c, mailbox, sink, e
}

// The PostgreSQL integration test consumes these actual dispatcher batches.
// This would fail with the old dispatcher, which emitted only SDK spans.
func TestExecutionTraceContract(t *testing.T) {
	d, c, m, sink, e := traceFixture(t)
	spanID := "5555555555555555"
	m.events = []messageprotocol.ObservationEvent{
		{MessageID: e.MessageID, Observation: messageprotocol.RuntimeObservation{Type: "span.started", SpanID: spanID, Name: "session.receive", Kind: "internal", StartedAt: d.now().Format(time.RFC3339Nano), Attributes: json.RawMessage(`{}`)}},
		{MessageID: e.MessageID, Observation: messageprotocol.RuntimeObservation{Type: "span.completed", SpanID: spanID, Status: "ok", FinishedAt: d.now().Format(time.RFC3339Nano), Attributes: json.RawMessage(`{}`)}},
	}
	d.observe = func(_ context.Context, r messagedelivery.ObservationRecord) error {
		if len(sink.batches) == 0 || len(sink.batches[0].Events) != 3 {
			t.Fatal("SDK event arrived before atomic lifecycle prerequisites")
		}
		event := observability.NewEvent(r.Observation.SpanID+":"+r.Observation.Type, r.Observation.Type, r.AppID, d.now())
		event.Source = "sdk"
		event.AttemptID, event.TraceID, event.SpanID = r.AttemptID, r.TraceID, r.Observation.SpanID
		event.Name, event.Kind, event.Status = r.Observation.Name, r.Observation.Kind, r.Observation.Status
		event.StartedAt, event.FinishedAt, event.Attributes = r.Observation.StartedAt, r.Observation.FinishedAt, r.Observation.Attributes
		return sink.Record(event)
	}
	if err := d.reconcile(context.Background(), []controlclient.MessageExecution{e}); err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, batch := range sink.batches {
		for _, event := range batch.Events {
			types = append(types, event.Type)
		}
	}
	want := []string{"message.accepted", "attempt.started", "attempt.routed", "span.started", "span.completed", "attempt.completed"}
	if !reflect.DeepEqual(types, want) {
		t.Fatalf("events = %v", types)
	}
	if c.saved[len(c.saved)-1].Phase != "quiescent" {
		t.Fatal("handler did not finish")
	}
	if _, exists := d.traceScopes.Load(e.Observability.AttemptID); exists {
		t.Fatal("terminal trace cache retained")
	}
	if file := os.Getenv("CANTELOP_TEST_TRACE_BATCHES"); file != "" {
		data, err := json.Marshal(sink.batches)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestExecutionTraceRestartReusesPersistedIdentity(t *testing.T) {
	d, _, _, sink, e := traceFixture(t)
	if !d.ensureExecutionObserved(context.Background(), e) {
		t.Fatal("initial trace failed")
	}
	if !d.ensureExecutionObserved(context.Background(), e) || len(sink.batches) != 1 {
		t.Fatal("cached trace duplicated")
	}
	restarted, _, _, _, _ := traceFixture(t)
	restarted.lifecycle = sink
	if !restarted.ensureExecutionObserved(context.Background(), e) {
		t.Fatal("restart trace failed")
	}
	if !reflect.DeepEqual(sink.batches[0], sink.batches[1]) {
		t.Fatal("restart changed trace identity or timestamps")
	}
}

func TestExecutionTraceRecordsTerminalFailures(t *testing.T) {
	for _, state := range []string{"failed", "timed_out", "sandbox_lost"} {
		t.Run(state, func(t *testing.T) {
			d, c, m, sink, e := traceFixture(t)
			m.receipt.State = messageprotocol.ReceiptState(state)
			if state == "sandbox_lost" {
				c.routeErr = &controlclient.Error{StatusCode: 404}
			}
			if err := d.reconcile(context.Background(), []controlclient.MessageExecution{e}); err != nil {
				t.Fatal(err)
			}
			last := sink.batches[len(sink.batches)-1].Events[0]
			wantCode := map[string]string{"failed": "handler_failed", "timed_out": "message_timed_out", "sandbox_lost": "sandbox_lost"}[state]
			if last.Type != "attempt.failed" || last.ErrorCode != wantCode || last.Incomplete != (state == "sandbox_lost") {
				t.Fatalf("terminal event: %#v", last)
			}
		})
	}
}

func TestExecutionTraceFailureDoesNotBlockCompletion(t *testing.T) {
	d, c, m, sink, e := traceFixture(t)
	sink.err = errors.New("spool full")
	m.events = []messageprotocol.ObservationEvent{{MessageID: e.MessageID}}
	d.observe = func(context.Context, messagedelivery.ObservationRecord) error {
		t.Fatal("orphaned SDK event was emitted")
		return nil
	}
	if err := d.reconcile(context.Background(), []controlclient.MessageExecution{e}); err != nil {
		t.Fatal(err)
	}
	if c.saved[len(c.saved)-1].Phase != "quiescent" {
		t.Fatal("telemetry prevented completion")
	}
	sink.err = nil
	if !d.ensureExecutionObserved(context.Background(), e) {
		t.Fatal("failed spool was cached instead of retried")
	}
}

func TestExecutionTraceDropsUnmatchedSDKEvents(t *testing.T) {
	d, _, m, _, e := traceFixture(t)
	m.events = []messageprotocol.ObservationEvent{{MessageID: "msg_99999999999999999999999999999999"}}
	d.observe = func(context.Context, messagedelivery.ObservationRecord) error {
		t.Fatal("stale event inherited current attempt")
		return nil
	}
	if err := d.reconcile(context.Background(), []controlclient.MessageExecution{e}); err != nil {
		t.Fatal(err)
	}
}

type settledObservationControl struct {
	*executionTestControl
	reads map[string]int
}

func (c *settledObservationControl) GetMessageDelivery(_ context.Context, id string) (controlclient.MessageDelivery, error) {
	c.reads[id]++
	return controlclient.MessageDelivery{ID: id, WorkspaceID: "workspace", LogicalSessionID: "thread"}, nil
}

func TestSettledSiblingActivityObservationsRetainTheirMessageIdentity(t *testing.T) {
	d, base, mailbox, sink, current := traceFixture(t)
	control := &settledObservationControl{executionTestControl: base, reads: make(map[string]int)}
	d.control = control
	mailbox.quiescent = false
	old := current
	old.MessageID, old.Phase = "msg_99999999999999999999999999999999", "settled"
	trace, err := messageprotocol.NewObservabilityContext("req_99999999999999999999999999999999", 1)
	if err != nil {
		t.Fatal(err)
	}
	old.Observability = &trace
	receipt := mailbox.receipt
	receipt.MessageID, receipt.AttemptID = old.MessageID, trace.AttemptID
	old.Receipt = &receipt
	// Cached prerequisites must not make the lazily resolved Message disappear.
	for _, e := range []controlclient.MessageExecution{current, old} {
		if !d.ensureExecutionObserved(context.Background(), e) {
			t.Fatal("missing trace prerequisites")
		}
	}
	clear(control.reads)
	mailbox.events = []messageprotocol.ObservationEvent{{MessageID: old.MessageID}}
	observed := 0
	d.observe = func(_ context.Context, record messagedelivery.ObservationRecord) error {
		observed++
		if record.MessageID != old.MessageID || record.AttemptID != trace.AttemptID || record.WorkspaceID != "workspace" || record.SessionID != "thread" {
			t.Fatalf("settled activity inherited the wrong identity: %+v", record)
		}
		return nil
	}
	if err := d.reconcile(context.Background(), []controlclient.MessageExecution{current, old}); err != nil {
		t.Fatal(err)
	}
	if observed != 1 || control.reads[old.MessageID] != 1 || len(sink.batches) != 2 {
		t.Fatalf("settled observations: emitted=%d Message reads=%d", observed, control.reads[old.MessageID])
	}
}
