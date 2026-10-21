package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/stepandel/cantelop/fire-fuse/controlclient"
	"github.com/stepandel/cantelop/fire-fuse/messagedelivery"
	"github.com/stepandel/cantelop/fire-fuse/messageprotocol"
	"github.com/stepandel/cantelop/fire-fuse/runtime/session"
	"github.com/stepandel/cantelop/fire-fuse/sessionbroker"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"
)

type executionTestControl struct {
	ExecutionControl
	saved              []controlclient.MessageExecution
	terminated         []string
	released           int
	releaseErr         error
	routeErr           error
	claimed            *controlclient.MessageDelivery
	failedCode         string
	requeues           int
	output             chan controlclient.SessionEventAppend
	replacementSandbox string
	message            controlclient.MessageDelivery
	savedReply         []byte
}

func (c *executionTestControl) ClaimNextMessageDelivery(context.Context) (*controlclient.MessageDelivery, error) {
	return c.claimed, nil
}
func (c *executionTestControl) FailMessageDelivery(_ context.Context, _, _, code string) error {
	c.failedCode = code
	return nil
}
func (c *executionTestControl) RequeueMessageDelivery(context.Context, string) error {
	c.requeues++
	return nil
}

func TestAdmissionRetriesUseDeadlineInsteadOfAttemptLimit(t *testing.T) {
	d, c, _, e := executionFixture(t)
	m := controlclient.MessageDelivery{ID: e.MessageID, Attempts: 6, AcceptedAt: d.now().Add(-23 * time.Second)}
	cause := &controlclient.Error{StatusCode: http.StatusTooManyRequests}
	if err := d.retryUnbound(context.Background(), m, cause); err == nil || c.requeues != 1 || c.failedCode != "" {
		t.Fatal("sixth retry exhausted the admission window early")
	}
	m.AcceptedAt = d.now().Add(-30 * time.Second)
	if err := d.retryUnbound(context.Background(), m, cause); err != nil || c.failedCode != "capacity_unavailable" {
		t.Fatalf("expired capacity failure: %s %v", c.failedCode, err)
	}
}

func TestExpiredAdmissionDoesNotAttemptAnotherSessionStart(t *testing.T) {
	d, c, _, e := executionFixture(t)
	c.claimed = &controlclient.MessageDelivery{ID: e.MessageID, Attempts: 7, AcceptedAt: d.now().Add(-30 * time.Second)}
	// All session methods are unimplemented: reaching one fails this test.
	if err := d.admitNext(context.Background()); err != nil || c.failedCode != "session_admission_timeout" {
		t.Fatalf("expired admission: %s %v", c.failedCode, err)
	}
}

func (c *executionTestControl) GetPinnedRoute(_ context.Context, id, sandbox string) (session.Route, error) {
	if sandbox == c.replacementSandbox && sandbox != "" {
		return session.Route{SandboxID: sandbox}, nil
	}
	return session.Route{SandboxID: sandbox}, c.routeErr
}
func (c *executionTestControl) GetWorkspace(context.Context, string) (controlclient.Workspace, error) {
	return controlclient.Workspace{AppID: "app_recovery"}, nil
}
func (c *executionTestControl) CreateSession(_ context.Context, spec controlclient.CreateSessionSpec) (controlclient.Session, error) {
	if c.replacementSandbox == "" {
		c.replacementSandbox = "sbx-99999999999999999999999999999999"
	}
	return controlclient.Session{ID: spec.ID, WorkspaceID: spec.WorkspaceID, KeepAliveSeconds: spec.KeepAliveSeconds}, nil
}
func (c *executionTestControl) GetSessionRoute(context.Context, string) (session.Route, error) {
	return session.Route{SandboxID: c.replacementSandbox}, nil
}
func (c *executionTestControl) GetMessageDelivery(context.Context, string) (controlclient.MessageDelivery, error) {
	if c.message.ID != "" {
		return c.message, nil
	}
	return controlclient.MessageDelivery{WorkspaceID: "workspace", LogicalSessionID: "thread"}, nil
}
func (c *executionTestControl) SaveMessageReply(_ context.Context, _ string, reply json.RawMessage) error {
	c.savedReply = append([]byte(nil), reply...)
	c.message.Reply = append([]byte(nil), reply...)
	return nil
}
func (c *executionTestControl) SaveExecution(_ context.Context, e controlclient.MessageExecution) (controlclient.MessageExecution, error) {
	e.Revision++
	c.saved = append(c.saved, e)
	return e, nil
}
func (c *executionTestControl) PinnedWorkLease(context.Context, string, string, string, bool) error {
	return nil
}
func (c *executionTestControl) ReleasePinnedWorkLease(context.Context, string, string, string) error {
	c.released++
	return c.releaseErr
}
func (c *executionTestControl) TerminateSandbox(_ context.Context, id string) error {
	c.terminated = append(c.terminated, id)
	return nil
}

// WaitForMessageDeliveries never notifies; admission relies on its interval.
func (c *executionTestControl) WaitForMessageDeliveries(ctx context.Context, after uint64) (uint64, error) {
	<-ctx.Done()
	return after, ctx.Err()
}
func (c *executionTestControl) AppendSessionEvent(_ context.Context, event controlclient.SessionEventAppend) (controlclient.SessionEvent, error) {
	if c.output != nil {
		c.output <- event
	}
	return controlclient.SessionEvent{}, nil
}

type executionTestMailbox struct {
	Mailbox
	receipt                          messageprotocol.Receipt
	statusErr, inspectErr, cancelErr error
	admissions, cancels              int
	quiescent                        bool
	outputPosition                   messageprotocol.StreamPosition
	observationPosition              messageprotocol.StreamPosition
	onCollect                        func() error
	recoveries                       int
	recoveryReceipt                  messageprotocol.RecoveryReceipt
	replyErr                         error
}

func (m *executionTestMailbox) Reply(context.Context, session.Route, string) (messageprotocol.MessageReply, error) {
	return messageprotocol.MessageReply{Reply: []byte(`{"ok":true}`)}, m.replyErr
}

func (m *executionTestMailbox) Inspect(_ context.Context, r session.Route) (messageprotocol.RuntimeStatus, error) {
	return messageprotocol.RuntimeStatus{SandboxID: r.SandboxID, Protocol: 2, Generation: 1, Quiescent: m.quiescent, Events: m.outputPosition, Observations: m.observationPosition, Capabilities: messageprotocol.RuntimeCapabilities{Replies: true}}, m.inspectErr
}
func (m *executionTestMailbox) Status(context.Context, session.Route, string) (messageprotocol.Receipt, error) {
	return m.receipt, m.statusErr
}
func (m *executionTestMailbox) Admit(context.Context, session.Route, messageprotocol.Request, time.Time) (messageprotocol.Receipt, error) {
	m.admissions++
	return m.receipt, nil
}
func (m *executionTestMailbox) Recover(_ context.Context, _ session.Route, request messageprotocol.RecoveryRequest) (messageprotocol.RecoveryReceipt, error) {
	m.recoveries++
	if m.recoveryReceipt.RecoveryID == "" {
		m.recoveryReceipt = messageprotocol.RecoveryReceipt{RecoveryID: request.RecoveryID, Generation: 1, State: "completed"}
	}
	return m.recoveryReceipt, nil
}
func (m *executionTestMailbox) Cancel(context.Context, session.Route, string) error {
	m.cancels++
	return m.cancelErr
}
func (m *executionTestMailbox) CollectObservations(context.Context, session.Route, func(messageprotocol.ObservationEvent) error) error {
	if m.onCollect != nil {
		return m.onCollect()
	}
	return nil
}
func executionFixture(t *testing.T) (*ExecutionDispatcher, *executionTestControl, *executionTestMailbox, controlclient.MessageExecution) {
	t.Helper()
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	e := controlclient.MessageExecution{MessageID: "msg_11111111111111111111111111111111", SandboxID: "sbx-22222222222222222222222222222222", SessionID: "ses_33333333333333333333333333333333", Attempt: 1, Revision: 1, Phase: "sending", AdmissionDeadline: now.Add(30 * time.Second), ExecutionDeadline: now.Add(5 * time.Minute), UpdatedAt: now}
	c := &executionTestControl{}
	m := &executionTestMailbox{receipt: messageprotocol.Receipt{MessageID: e.MessageID, SandboxID: e.SandboxID, Sequence: 1, Generation: 1, AcceptedAt: now, Deadline: e.ExecutionDeadline, State: "running"}}
	d := &ExecutionDispatcher{control: c, mailbox: m, output: idleOutputCollector{}, outputs: map[string]outputCollection{}, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), now: func() time.Time { return now }, renewed: map[string]time.Time{}}
	d.broker, _ = sessionbroker.New(c)
	t.Cleanup(func() { d.stopOutputCollectors(nil) })
	return d, c, m, e
}
func TestExecutionRecoversLostACKWithoutReadmission(t *testing.T) {
	d, c, m, e := executionFixture(t)
	if err := d.reconcile(context.Background(), []controlclient.MessageExecution{e}); err != nil {
		t.Fatal(err)
	}
	if m.admissions != 0 || len(c.saved) != 3 || c.saved[len(c.saved)-1].Phase != "running" || c.saved[len(c.saved)-1].Receipt.Sequence != 1 {
		t.Fatalf("recovery: %#v admissions %d", c.saved, m.admissions)
	}
}
func TestExecutionPersistsReplyBeforeHandledProjection(t *testing.T) {
	d, c, m, e := executionFixture(t)
	c.message = controlclient.MessageDelivery{ID: e.MessageID, WorkspaceID: "workspace", LogicalSessionID: "thread", ReplyRequested: true}
	m.receipt.State = "succeeded"
	if err := d.reconcile(context.Background(), []controlclient.MessageExecution{e}); err != nil {
		t.Fatal(err)
	}
	if string(c.savedReply) != `{"ok":true}` || len(c.saved) == 0 || c.saved[len(c.saved)-1].Phase != "settled" {
		t.Fatalf("reply=%s executions=%#v", c.savedReply, c.saved)
	}
}
func TestExecutionStaysUnsettledWhileReplyIsUnavailable(t *testing.T) {
	d, c, m, e := executionFixture(t)
	c.message = controlclient.MessageDelivery{ID: e.MessageID, WorkspaceID: "workspace", LogicalSessionID: "thread", ReplyRequested: true}
	m.receipt.State = "succeeded"
	m.replyErr = &messagedelivery.RuntimeResponseError{StatusCode: 409}
	if err := d.reconcile(context.Background(), []controlclient.MessageExecution{e}); err != nil {
		t.Fatal(err)
	}
	if len(c.savedReply) != 0 || len(c.saved) == 0 || c.saved[len(c.saved)-1].Phase == "settled" {
		t.Fatalf("reply=%s executions=%#v", c.savedReply, c.saved)
	}
}
func TestExecutionReAdmitsOnlyConfirmedAbsentReservation(t *testing.T) {
	d, c, m, e := executionFixture(t)
	m.statusErr = &messagedelivery.RuntimeResponseError{StatusCode: 404}
	if err := d.reconcile(context.Background(), []controlclient.MessageExecution{e}); err != nil {
		t.Fatal(err)
	}
	if m.admissions != 1 || len(c.saved) != 3 {
		t.Fatal("confirmed absence was not retried")
	}
}
func TestExecutionStatusOutageHasBoundedUnknownOutcome(t *testing.T) {
	d, c, m, e := executionFixture(t)
	m.statusErr = errors.New("response lost")
	d.now = func() time.Time { return e.AdmissionDeadline.Add(11 * time.Second) }
	if err := d.reconcile(context.Background(), []controlclient.MessageExecution{e}); err != nil {
		t.Fatal(err)
	}
	if len(c.terminated) != 1 || c.terminated[0] != e.SandboxID || c.saved[len(c.saved)-1].Phase != "unavailable" || m.admissions != 0 {
		t.Fatal("ambiguous delivery was not fenced")
	}
}
func TestExecutionReplacementNeverAdmitsToNewSandbox(t *testing.T) {
	d, c, m, e := executionFixture(t)
	c.routeErr = &controlclient.Error{StatusCode: http.StatusConflict}
	if err := d.reconcile(context.Background(), []controlclient.MessageExecution{e}); err != nil {
		t.Fatal(err)
	}
	if m.admissions != 0 || c.saved[len(c.saved)-1].Phase != "unavailable" || len(c.terminated) != 0 {
		t.Fatal("replacement touched")
	}
}

func TestExecutionInvokesRecoveryHookOnReplacementSandbox(t *testing.T) {
	d, c, m, e := executionFixture(t)
	e.RecoverySupported = true
	c.routeErr = &controlclient.Error{StatusCode: http.StatusConflict}
	m.quiescent = true
	if err := d.reconcile(context.Background(), []controlclient.MessageExecution{e}); err != nil {
		t.Fatal(err)
	}
	if m.recoveries != 1 || c.replacementSandbox == "" {
		t.Fatalf("recovery invocations=%d replacement=%q", m.recoveries, c.replacementSandbox)
	}
	if len(c.saved) == 0 || c.saved[len(c.saved)-1].Phase != "unavailable" ||
		c.saved[len(c.saved)-1].ErrorCode != "sandbox_lost_recovered" {
		t.Fatalf("recovered execution = %#v", c.saved)
	}
	foundDurableIntent := false
	for _, saved := range c.saved {
		if saved.Phase == "recovering" && saved.Recovery != nil && saved.Recovery.SandboxID == c.replacementSandbox {
			foundDurableIntent = true
		}
	}
	if !foundDurableIntent {
		t.Fatal("replacement binding was not persisted before recovery")
	}
}

func TestExecutionResumesPersistedRecoveryWithoutReinvokingHook(t *testing.T) {
	d, c, m, e := executionFixture(t)
	c.routeErr = &controlclient.Error{StatusCode: http.StatusConflict}
	c.replacementSandbox = "sbx-99999999999999999999999999999999"
	e.Phase = "recovering"
	e.ErrorCode = "sandbox_lost"
	e.RecoverySupported = true
	e.Recovery = &messageprotocol.RecoveryState{
		ID: recoveryIdentity(e.SandboxID), SandboxID: c.replacementSandbox, Generation: 1,
	}
	m.quiescent = true
	if err := d.reconcile(context.Background(), []controlclient.MessageExecution{e}); err != nil {
		t.Fatal(err)
	}
	if m.recoveries != 0 || c.saved[len(c.saved)-1].Phase != "unavailable" {
		t.Fatalf("persisted recovery replayed hook: invocations=%d saved=%#v", m.recoveries, c.saved)
	}
}
func TestExecutionCancellationFailureStillTerminatesAfterGrace(t *testing.T) {
	d, c, m, e := executionFixture(t)
	e.Phase = "running"
	e.Receipt = &m.receipt
	m.cancelErr = errors.New("ignored")
	d.now = func() time.Time { return e.ExecutionDeadline.Add(11 * time.Second) }
	if err := d.reconcile(context.Background(), []controlclient.MessageExecution{e}); err != nil {
		t.Fatal(err)
	}
	if len(c.terminated) != 1 {
		t.Fatal("cancellation error defeated deadline")
	}
}
func TestExecutionTerminalWriteWaitsForLeaseRelease(t *testing.T) {
	d, c, m, e := executionFixture(t)
	m.receipt.State = "succeeded"
	m.quiescent = true
	c.releaseErr = errors.New("unavailable")
	if err := d.reconcile(context.Background(), []controlclient.MessageExecution{e}); err == nil {
		t.Fatal("release failure missing")
	}
	// First inspection and settlement save; the unchanged settled re-inspection does not.
	if len(c.saved) != 2 || c.saved[len(c.saved)-1].Phase != "settled" {
		t.Fatal("terminal receipt hid pending release")
	}
}

func TestExecutionQuiescenceWaitsForSessionOutputHandoff(t *testing.T) {
	d, c, m, e := executionFixture(t)
	m.receipt.State = "succeeded"
	m.quiescent = true
	e.Phase = "settled"
	e.Receipt = &m.receipt
	e.PhaseAt = d.now()
	m.outputPosition = messageprotocol.StreamPosition{Acknowledged: 1, Earliest: 2, Latest: 2}
	if err := d.reconcile(context.Background(), []controlclient.MessageExecution{e}); err != nil {
		t.Fatal(err)
	}
	settled := c.saved[len(c.saved)-1]
	if settled.Phase != "settled" || c.released != 0 {
		t.Fatalf("pending Session output allowed quiescence: %#v", settled)
	}
	m.outputPosition.Acknowledged = 2
	m.outputPosition.Earliest = 3
	if err := d.reconcile(context.Background(), []controlclient.MessageExecution{settled}); err != nil {
		t.Fatal(err)
	}
	if c.saved[len(c.saved)-1].Phase != "quiescent" || c.released != 1 {
		t.Fatal("completed Session output handoff did not allow quiescence")
	}
}

func TestSessionOutputContinuesDuringMailboxReconciliationFailure(t *testing.T) {
	d, c, m, e := executionFixture(t)
	c.output = make(chan controlclient.SessionEventAppend, 1)
	d.output = immediateOutputCollector{event: messageprotocol.OutputEvent{Cursor: 1, MessageID: e.MessageID, Data: []byte(`{"delta":"hello"}`)}}
	m.inspectErr = errors.New("mailbox status unavailable")
	if err := d.reconcile(context.Background(), []controlclient.MessageExecution{e}); err == nil {
		t.Fatal("mailbox failure missing")
	}
	select {
	case event := <-c.output:
		if event.SourceCursor != 1 || event.SourceSandboxID != e.SandboxID {
			t.Fatalf("Session output handoff = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("mailbox reconciliation paced Session output")
	}
}

type idleOutputCollector struct{}

func (idleOutputCollector) Collect(ctx context.Context, _ session.Route, _ func(messageprotocol.OutputEvent) error) error {
	<-ctx.Done()
	return ctx.Err()
}

type immediateOutputCollector struct{ event messageprotocol.OutputEvent }

func (c immediateOutputCollector) Collect(ctx context.Context, _ session.Route, appendEvent func(messageprotocol.OutputEvent) error) error {
	if err := appendEvent(c.event); err != nil {
		return err
	}
	<-ctx.Done()
	return ctx.Err()
}

func TestFinalActivityObservationCollectedBeforeQuiescentRelease(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		t.Run(fmt.Sprint(unavailable), func(t *testing.T) {
			d, c, m, e := executionFixture(t)
			m.receipt.State = "succeeded"
			collections := 0
			m.onCollect = func() error {
				collections++
				if c.released != 0 {
					t.Fatal("originating attempt released before final observation collection")
				}
				if collections == 1 {
					// The activity settles just after the collector's first snapshot.
					m.quiescent = true
					m.observationPosition = messageprotocol.StreamPosition{Latest: 1, Earliest: 1}
				} else if unavailable {
					return errors.New("telemetry unavailable")
				}
				return nil
			}
			if err := d.reconcile(context.Background(), []controlclient.MessageExecution{e}); err != nil {
				t.Fatal(err)
			}
			if collections != 2 {
				t.Fatalf("final activity observation missed: %d collections", collections)
			}
			if c.released != 1 || c.saved[len(c.saved)-1].Phase != "quiescent" || m.admissions != 0 {
				t.Fatal("final observation collection changed release or delivery semantics")
			}
		})
	}
}

func TestLostIntakeRetriesOnlyOptedInUnacknowledgedWork(t *testing.T) {
	for _, tc := range []struct {
		name    string
		optedIn bool
		phase   messageprotocol.ExecutionPhase
		want    messageprotocol.ExecutionPhase
	}{
		{"idempotent intake", true, "running", "retrying"},
		{"acknowledged intake", true, "settled", "unavailable"},
		{"intake without redelivery opt-in", false, "running", "unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, c, mailbox, e := executionFixture(t)
			e.Receipt = &mailbox.receipt
			e.RedeliverySupported = tc.optedIn
			e.Phase = tc.phase
			e.Receipt.State = "running"
			if tc.phase == "settled" {
				e.Receipt.State = "succeeded"
			}
			if err := d.retryLostIntake(context.Background(), []controlclient.MessageExecution{e}); err != nil {
				t.Fatal(err)
			}
			if len(c.terminated) != 1 || c.terminated[0] != e.SandboxID {
				t.Fatal("did not fence exact owner")
			}
			if len(c.saved) != 1 || c.saved[0].Phase != tc.want {
				t.Fatalf("saved=%#v", c.saved)
			}
		})
	}
}
