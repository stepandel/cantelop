package dispatcher

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/controlclient"
)

type wakeSaveControl struct {
	*executionTestControl
	beforeSave func()
	saveErr    error
}

func (c *wakeSaveControl) SaveExecution(ctx context.Context, e controlclient.MessageExecution) (controlclient.MessageExecution, error) {
	if c.beforeSave != nil {
		c.beforeSave()
	}
	if c.saveErr != nil {
		return e, c.saveErr
	}
	return c.executionTestControl.SaveExecution(ctx, e)
}

func TestAdmissionWakesOnlyAfterSuccessfulPersistence(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "saved", true: "failed"}[failed], func(t *testing.T) {
			d, c, _, e := executionFixture(t)
			d.reconcileWake = make(chan struct{}, 1)
			c.claimed = &controlclient.MessageDelivery{ID: e.MessageID, WorkspaceID: "workspace", LogicalSessionID: "thread", Attempts: 1, AcceptedAt: d.now()}
			wrapped := &wakeSaveControl{executionTestControl: c, beforeSave: func() {
				if len(d.reconcileWake) != 0 {
					t.Fatal("woke before persistence completed")
				}
			}}
			if failed {
				wrapped.saveErr = errors.New("persistence unavailable")
			}
			d.control = wrapped
			err := d.admitNext(context.Background())
			if failed {
				if err == nil || len(d.reconcileWake) != 0 {
					t.Fatal("failed persistence scheduled delivery")
				}
			} else if err != nil || len(d.reconcileWake) != 1 {
				t.Fatalf("saved admission did not wake reconciliation: %v", err)
			}
		})
	}
}

func TestReconciliationWakeCoalescesAndSurvivesActivePass(t *testing.T) {
	d, _, _, _ := executionFixture(t)
	d.reconcileWake = make(chan struct{}, 1)
	d.interval = time.Hour
	for i := 0; i < 100; i++ {
		d.wakeReconciliation()
	}
	if len(d.reconcileWake) != 1 {
		t.Fatal("wakes did not coalesce")
	}
	if !d.waitForReconciliation(context.Background()) {
		t.Fatal("queued wake was lost")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if d.waitForReconciliation(ctx) {
		t.Fatal("cancellation did not stop wait")
	}
}

type wakeRunControl struct {
	*executionTestControl
	mu        sync.Mutex
	delivered bool
	claimGate chan struct{}
	listed    chan struct{}
}

func (c *wakeRunControl) ClaimNextMessageDelivery(ctx context.Context) (*controlclient.MessageDelivery, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.delivered {
		return nil, nil
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.claimGate:
	}
	c.delivered = true
	return c.claimed, nil
}
func (c *wakeRunControl) RequeueHandlingMessageDeliveries(context.Context) (int64, error) {
	return 0, nil
}
func (c *wakeRunControl) ListExecutions(context.Context) ([]controlclient.MessageExecution, error) {
	c.listed <- struct{}{}
	return nil, nil
}

func TestRunReconcilesNewAdmissionWithoutWaitingForInterval(t *testing.T) {
	_, base, mailbox, e := executionFixture(t)
	base.claimed = &controlclient.MessageDelivery{ID: e.MessageID, WorkspaceID: "workspace", LogicalSessionID: "thread", Attempts: 1, AcceptedAt: time.Now()}
	c := &wakeRunControl{executionTestControl: base, claimGate: make(chan struct{}), listed: make(chan struct{}, 10)}
	d, err := NewExecutionDispatcher(c, mailbox, idleOutputCollector{}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	d.interval = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(2 * time.Second):
			t.Error("dispatcher failed to stop")
		}
	}()
	select {
	case <-c.listed:
	case <-time.After(2 * time.Second):
		t.Fatal("initial pass did not run")
	}
	close(c.claimGate)
	select {
	case <-c.listed:
	case <-time.After(2 * time.Second):
		t.Fatal("saved admission waited for the hourly interval")
	}
}

func TestAdmissionReconciliationRetriesWhenWorkerBecomesAvailable(t *testing.T) {
	for _, saturated := range []bool{false, true} {
		t.Run(map[bool]string{false: "same Sandbox busy", true: "all workers busy"}[saturated], func(t *testing.T) {
			d, _, _, e := executionFixture(t)
			d.reconcileWake = make(chan struct{}, 1)
			d.completionWake = make(chan struct{}, 1)
			w := newReconciliationWorkers()
			w.onAvailable = func() { d.wakePendingReconciliations(w) }
			blocked := make(chan struct{})
			var release sync.Once
			defer func() { release.Do(func() { close(blocked) }); w.wg.Wait() }()
			if saturated {
				for i := range 16 {
					if !w.start(fmt.Sprint(i), func() { <-blocked }) {
						t.Fatal("worker capacity exhausted early")
					}
				}
			} else if !d.startReconciliation(w, e.SandboxID, func() { <-blocked }) {
				t.Fatal("initial reconciliation did not start")
			}

			// A new durable admission wakes a full pass, which sees the execution
			// but cannot start its worker yet. This must survive the consumed wake.
			d.requestReconciliation(e.SandboxID)
			if cause := d.waitForReconciliationCause(context.Background(), time.Now().Add(time.Hour)); cause != reconciliationFull {
				t.Fatalf("admission wake = %d", cause)
			}
			if d.startReconciliation(w, e.SandboxID, func() { t.Error("worker started without capacity") }) {
				t.Fatal("busy worker admitted reconciliation")
			}
			release.Do(func() { close(blocked) })
			w.wg.Wait()

			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			if cause := d.waitForReconciliationCause(ctx, time.Now().Add(time.Hour)); cause != reconciliationCompletion {
				t.Fatal("admission wake lost; delivery would wait for the fallback timer")
			}
			if d.startReconciliation(w, "unrelated", func() { t.Error("unrelated Sandbox reconciled") }, true) {
				t.Fatal("retry was not scoped to the waiting Sandbox")
			}
			ran := make(chan struct{})
			if !d.startReconciliation(w, e.SandboxID, func() { close(ran) }, true) {
				t.Fatal("waiting admission was not retried")
			}
			<-ran
			w.wg.Wait()
		})
	}
}
