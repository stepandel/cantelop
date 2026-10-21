package dispatcher

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/controlclient"
	"github.com/stepandel/cantelop/fire-fuse/messageprotocol"
	"github.com/stepandel/cantelop/fire-fuse/runtime/session"
)

type completionRunControl struct {
	*executionTestControl
	mu        sync.Mutex
	execution controlclient.MessageExecution
	handled   chan struct{}
	once      sync.Once
}

func (c *completionRunControl) ClaimNextMessageDelivery(context.Context) (*controlclient.MessageDelivery, error) {
	return nil, nil
}
func (c *completionRunControl) RequeueHandlingMessageDeliveries(context.Context) (int64, error) {
	return 0, nil
}
func (c *completionRunControl) ListExecutions(context.Context) ([]controlclient.MessageExecution, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return []controlclient.MessageExecution{c.execution}, nil
}
func (c *completionRunControl) GetMessageDelivery(ctx context.Context, id string) (controlclient.MessageDelivery, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.executionTestControl.GetMessageDelivery(ctx, id)
}
func (c *completionRunControl) SaveMessageReply(ctx context.Context, id string, r json.RawMessage) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.executionTestControl.SaveMessageReply(ctx, id, r)
}
func (c *completionRunControl) SaveExecution(ctx context.Context, e controlclient.MessageExecution) (controlclient.MessageExecution, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	updated, err := c.executionTestControl.SaveExecution(ctx, e)
	c.execution = updated
	if e.Phase == "settled" && err == nil && len(c.savedReply) > 0 {
		c.once.Do(func() { close(c.handled) })
	}
	return updated, err
}

type completionTestMailbox struct {
	*executionTestMailbox
	completed  chan struct{}
	subscribed chan struct{}
	once       sync.Once
	supported  bool
}

func (m *completionTestMailbox) Inspect(ctx context.Context, r session.Route) (messageprotocol.RuntimeStatus, error) {
	snapshot, err := m.executionTestMailbox.Inspect(ctx, r)
	snapshot.Capabilities.CompletionNotifications = m.supported
	return snapshot, err
}
func (m *completionTestMailbox) Status(ctx context.Context, r session.Route, id string) (messageprotocol.Receipt, error) {
	receipt := m.receipt
	select {
	case <-m.completed:
		receipt.State = "succeeded"
	default:
	}
	return receipt, nil
}
func (m *completionTestMailbox) WaitForCompletions(ctx context.Context, _ session.Route, after uint64) (uint64, error) {
	m.once.Do(func() { close(m.subscribed) })
	if after == 0 {
		select {
		case <-m.completed:
			return 1, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	<-ctx.Done()
	return after, ctx.Err()
}

func TestRuntimeCompletionPersistsReplyWithoutReconciliationTick(t *testing.T) {
	d, base, mailbox, e := executionFixture(t)
	e.Phase = "running"
	base.message = controlclient.MessageDelivery{ID: e.MessageID, WorkspaceID: "workspace", LogicalSessionID: "thread", ReplyRequested: true}
	control := &completionRunControl{executionTestControl: base, execution: e, handled: make(chan struct{})}
	runtime := &completionTestMailbox{executionTestMailbox: mailbox, completed: make(chan struct{}), subscribed: make(chan struct{}), supported: true}
	d.control = control
	d.mailbox = runtime
	d.interval = time.Hour
	d.reconcileWake = make(chan struct{}, 1)
	d.completionWake = make(chan struct{}, 1)
	d.admissionWake = make(chan struct{}, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	select {
	case <-runtime.subscribed:
	case <-time.After(time.Second):
		t.Fatal("completion subscription missing")
	}
	close(runtime.completed)
	select {
	case <-control.handled:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("durable reply waited for reconciliation tick")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("notification collector did not stop")
	}
}

func TestCompletionDuringActiveReconciliationSurvivesWorkerRelease(t *testing.T) {
	d, _, _, e := executionFixture(t)
	d.reconcileWake = make(chan struct{}, 1)
	d.completionWake = make(chan struct{}, 1)
	closed := make(chan struct{})
	close(closed)
	d.outputs[e.SandboxID] = outputCollection{cancel: func() {}, done: closed, owners: map[string]bool{e.SandboxID: true}}
	w := newReconciliationWorkers()
	w.onAvailable = func() { d.wakePendingReconciliations(w) }
	blocked, started := make(chan struct{}), make(chan struct{})
	d.startReconciliation(w, e.SandboxID, func() { close(started); <-blocked })
	<-started
	d.notifyCompletion(e.SandboxID)
	<-d.completionWake
	if d.startReconciliation(w, e.SandboxID, func() { t.Error("overlapping reconciliation") }) {
		t.Fatal("active worker overlapped")
	}
	close(blocked)
	w.wg.Wait()
	select {
	case <-d.completionWake:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("completion wake lost behind active worker")
	}
	ran := make(chan struct{})
	if !d.startReconciliation(w, e.SandboxID, func() { close(ran) }) {
		t.Fatal("pending completion could not run")
	}
	<-ran
	w.wg.Wait()
	if len(d.pendingReconciles) != 0 {
		t.Fatal("completion hint leaked")
	}
}

func TestLegacyRuntimeDoesNotSubscribeToCompletions(t *testing.T) {
	d, _, mailbox, _ := executionFixture(t)
	runtime := &completionTestMailbox{executionTestMailbox: mailbox, subscribed: make(chan struct{})}
	d.mailbox = runtime
	d.watchCompletions(context.Background(), session.Route{SandboxID: mailbox.receipt.SandboxID}, runtime)
	select {
	case <-runtime.subscribed:
		t.Fatal("legacy runtime endpoint probed")
	default:
	}
}

func TestCompletionWaitsForCapacityWithoutLosingWake(t *testing.T) {
	d, _, _, e := executionFixture(t)
	d.reconcileWake = make(chan struct{}, 1)
	d.completionWake = make(chan struct{}, 1)
	closed := make(chan struct{})
	close(closed)
	d.outputs[e.SandboxID] = outputCollection{cancel: func() {}, done: closed, owners: map[string]bool{e.SandboxID: true}}
	w := newReconciliationWorkers()
	w.onAvailable = func() { d.wakePendingReconciliations(w) }
	blocked := make(chan struct{})
	for i := 0; i < 16; i++ {
		if !w.start(fmt.Sprint(i), func() { <-blocked }) {
			t.Fatal("capacity missing")
		}
	}
	d.notifyCompletion(e.SandboxID)
	<-d.completionWake
	if d.startReconciliation(w, e.SandboxID, func() {}) {
		t.Fatal("capacity exceeded")
	}
	close(blocked)
	w.wg.Wait()
	select {
	case <-d.completionWake:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("completion did not wake after capacity returned")
	}
}

func TestRecoveryCompletionsNotifyDurableAndNewOwners(t *testing.T) {
	d, _, _, e := executionFixture(t)
	d.reconcileWake = make(chan struct{}, 1)
	d.completionWake = make(chan struct{}, 1)
	closed := make(chan struct{})
	close(closed)
	replacement := "sbx-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	d.outputs[replacement] = outputCollection{cancel: func() {}, done: closed, owners: map[string]bool{e.SandboxID: true, replacement: true}}
	d.notifyCompletion(replacement)
	if !d.pendingReconciles[e.SandboxID] || !d.pendingReconciles[replacement] {
		t.Fatal("recovery completion lost an owner")
	}
}

func TestCompletionWakeIsScopedAndDoesNotPostponeSupervision(t *testing.T) {
	d, _, _, _ := executionFixture(t)
	d.completionWake = make(chan struct{}, 1)
	w := newReconciliationWorkers()
	if d.startReconciliation(w, "unrelated", func() { t.Error("unrelated Sandbox reconciled") }, true) {
		t.Fatal("notification reconciled unrelated Sandbox")
	}
	d.wakeCompletion()
	if cause := d.waitForReconciliationCause(context.Background(), time.Now().Add(time.Hour)); cause != reconciliationCompletion {
		t.Fatal(cause)
	}
	d.wakeCompletion()
	if cause := d.waitForReconciliationCause(context.Background(), time.Now().Add(-time.Second)); cause != reconciliationFull {
		t.Fatal("completion traffic postponed fallback supervision")
	}
}
