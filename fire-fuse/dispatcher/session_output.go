package dispatcher

import (
	"context"
	"sync"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/controlclient"
	"github.com/stepandel/cantelop/fire-fuse/messageprotocol"
	"github.com/stepandel/cantelop/fire-fuse/runtime/session"
)

func (d *ExecutionDispatcher) ensureOutputCollector(ctx context.Context, route session.Route, workspaceID, logicalSessionID, ownerSandboxID string) {
	d.outputMu.Lock()
	if existing, ok := d.outputs[route.SandboxID]; ok {
		existing.owners[ownerSandboxID] = true
		d.outputMu.Unlock()
		return
	}
	collectorContext, cancel := context.WithCancel(ctx)
	current := outputCollection{cancel: cancel, done: make(chan struct{}), owners: map[string]bool{ownerSandboxID: true}}
	d.outputs[route.SandboxID] = current
	d.outputMu.Unlock()
	go func() {
		defer close(current.done)
		var notifications sync.WaitGroup
		if waiter, ok := d.mailbox.(completionWaiter); ok {
			notifications.Add(1)
			go func() { defer notifications.Done(); d.watchCompletions(collectorContext, route, waiter) }()
		}
		defer notifications.Wait()
		defer cancel()
		for collectorContext.Err() == nil {
			err := d.output.Collect(collectorContext, route, func(event messageprotocol.OutputEvent) error {
				_, err := d.control.AppendSessionEvent(collectorContext, controlclient.SessionEventAppend{
					WorkspaceID: workspaceID, SessionID: logicalSessionID, MessageID: event.MessageID,
					View: event.View, Data: event.Data, SourceSandboxID: route.SandboxID, SourceCursor: event.Cursor,
				})
				return err
			})
			if collectorContext.Err() != nil {
				return
			}
			d.logger.Warn("Session output handoff deferred", "sandbox_id", route.SandboxID, "error", err)
			// Failure backoff is independent of lifecycle reconciliation.
			if !waitMessageRetry(collectorContext, time.Second) {
				return
			}
		}
	}()
}

func (d *ExecutionDispatcher) stopOutputCollector(sandboxID string) {
	d.outputMu.Lock()
	collector, exists := d.outputs[sandboxID]
	if exists {
		collector.cancel()
		delete(d.outputs, sandboxID)
	}
	d.outputMu.Unlock()
	if exists {
		<-collector.done
	}
}

func (d *ExecutionDispatcher) stopOutputCollectors(active map[string]bool) {
	d.outputMu.Lock()
	var stopped []outputCollection
	for sandboxID, collector := range d.outputs {
		if active == nil || !active[sandboxID] {
			collector.cancel()
			stopped = append(stopped, collector)
			delete(d.outputs, sandboxID)
		}
	}
	d.outputMu.Unlock()
	for _, collector := range stopped {
		<-collector.done
	}
}
