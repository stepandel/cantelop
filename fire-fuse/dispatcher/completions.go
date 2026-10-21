package dispatcher

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/messagedelivery"
	"github.com/stepandel/cantelop/fire-fuse/runtime/session"
)

type completionWaiter interface {
	WaitForCompletions(context.Context, session.Route, uint64) (uint64, error)
}

func (d *ExecutionDispatcher) watchCompletions(ctx context.Context, route session.Route, waiter completionWaiter) {
	// Older SDKs keep the ordinary reconciliation timer. Never probe an
	// unsupported endpoint or use quiescence/telemetry as handler completion.
	for {
		snapshot, err := d.mailbox.Inspect(ctx, route)
		if err == nil {
			if !snapshot.Capabilities.CompletionNotifications {
				return
			}
			break
		}
		if !waitMessageRetry(ctx, time.Second) {
			return
		}
	}
	var after uint64
	for ctx.Err() == nil {
		generation, err := waiter.WaitForCompletions(ctx, route, after)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			var response *messagedelivery.RuntimeResponseError
			if errors.As(err, &response) && response.StatusCode == http.StatusRequestTimeout {
				continue
			}
			d.logger.Warn("Runtime completion notification deferred", "sandbox_id", route.SandboxID, "error", err)
			if !waitMessageRetry(ctx, time.Second) {
				return
			}
			continue
		}
		if generation > after {
			after = generation
			d.notifyCompletion(route.SandboxID)
		}
	}
}

func (d *ExecutionDispatcher) notifyCompletion(runtimeSandboxID string) {
	// A recovery Sandbox can host executions whose durable owner is the old
	// Sandbox. Notify every current owner registered on the output collector.
	d.outputMu.Lock()
	owners := d.outputs[runtimeSandboxID].owners
	d.reconcileMu.Lock()
	if d.pendingReconciles == nil {
		d.pendingReconciles = make(map[string]bool)
	}
	for owner := range owners {
		d.pendingReconciles[owner] = true
	}
	d.reconcileMu.Unlock()
	d.outputMu.Unlock()
	d.wakeCompletion()
}

// A new admission can find its Sandbox still busy or all worker slots occupied.
// Retain it like a completion so the release callback retries it; otherwise the
// admission wake is consumed and delivery waits for the timer. Busy Sandboxes
// without new work keep waiting for the timer instead of rerunning back to back.
func (d *ExecutionDispatcher) requestReconciliation(sandboxID string) {
	d.reconcileMu.Lock()
	if d.pendingReconciles == nil {
		d.pendingReconciles = make(map[string]bool)
	}
	d.pendingReconciles[sandboxID] = true
	d.reconcileMu.Unlock()
	d.wakeReconciliation()
}

func (d *ExecutionDispatcher) startReconciliation(w *reconciliationWorkers, id string, run func(), pendingOnly ...bool) bool {
	// Serialize consuming a notification with worker release. If this Sandbox
	// is already reconciling, retain the hint until its worker becomes available.
	d.reconcileMu.Lock()
	defer d.reconcileMu.Unlock()
	if len(pendingOnly) > 0 && pendingOnly[0] && !d.pendingReconciles[id] {
		return false
	}
	started := w.start(id, run)
	if started {
		delete(d.pendingReconciles, id)
	}
	return started
}

func (d *ExecutionDispatcher) wakePendingReconciliations(w *reconciliationWorkers) {
	d.reconcileMu.Lock()
	defer d.reconcileMu.Unlock()
	for id := range d.pendingReconciles {
		if !w.isRunning(id) {
			d.wakeCompletion()
			return
		}
	}
}

func (d *ExecutionDispatcher) wakeCompletion() {
	select {
	case d.completionWake <- struct{}{}:
	default:
	}
}
