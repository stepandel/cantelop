package dispatcher

import (
	"context"
	"sync"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/controlclient"
	"github.com/stepandel/cantelop/fire-fuse/messageprotocol"
	"github.com/stepandel/cantelop/fire-fuse/runtime/session"
)

// This process-local handoff proves that no POST has been attempted. It is never
// reconstructed from durable phase="sending", which also covers ambiguous sends.
// Reconciliation owns consumption, retaining existing per-Sandbox serialization.
type freshDelivery struct {
	execution   controlclient.MessageExecution
	message     controlclient.MessageDelivery
	route       session.Route
	inspectedAt time.Time
}
type freshDeliveries struct {
	sync.Mutex
	pending map[string]freshDelivery
}

const freshDeliveryLifetime = time.Second

// Called with the unsaved execution; its first durable save becomes revision 1.
func (d *ExecutionDispatcher) rememberFreshDelivery(e controlclient.MessageExecution, m controlclient.MessageDelivery, route session.Route, inspectedAt time.Time) {
	if e.Revision != 0 || e.Phase != messageprotocol.PhaseSending || e.Receipt != nil || e.Attempt != 1 || m.Execution != nil {
		return
	}
	d.fresh.Lock()
	defer d.fresh.Unlock()
	if d.fresh.pending == nil {
		d.fresh.pending = make(map[string]freshDelivery)
	}
	for id, entry := range d.fresh.pending {
		if d.now().Sub(entry.inspectedAt) >= freshDeliveryLifetime {
			delete(d.fresh.pending, id)
		}
	}
	e.Revision = 1
	d.fresh.pending[e.MessageID] = freshDelivery{e, m, route, inspectedAt}
}

func (d *ExecutionDispatcher) forgetFreshDelivery(messageID string) {
	d.fresh.Lock()
	defer d.fresh.Unlock()
	delete(d.fresh.pending, messageID)
}

func (d *ExecutionDispatcher) takeFreshDelivery(group []controlclient.MessageExecution) (*freshDelivery, int) {
	d.fresh.Lock()
	defer d.fresh.Unlock()
	var candidate *freshDelivery
	index := 0
	unsettled := 0
	for _, e := range group {
		if e.Phase != messageprotocol.PhaseSettled {
			unsettled++
		}
	}
	for i, e := range group {
		entry, ok := d.fresh.pending[e.MessageID]
		delete(d.fresh.pending, e.MessageID) // Consume before any external call, even on failure.
		if !ok || unsettled != 1 || e.Phase != messageprotocol.PhaseSending || e.Receipt != nil || e.Revision != entry.execution.Revision || e.Attempt != entry.execution.Attempt || e.SandboxID != entry.execution.SandboxID || e.SessionID != entry.execution.SessionID || !e.ExecutionDeadline.Equal(entry.execution.ExecutionDeadline) || !e.AdmissionDeadline.Equal(entry.execution.AdmissionDeadline) {
			continue
		}
		if d.now().Before(entry.inspectedAt) || d.now().Sub(entry.inspectedAt) >= freshDeliveryLifetime || !d.now().Before(e.AdmissionDeadline) {
			continue
		}
		candidate, index = &entry, i
	}
	return candidate, index
}

// Send only the first locally proven attempt before ordinary reconciliation.
// Route and capability validation, lease acquisition, and durable ownership have
// already succeeded. Runtime requests fence the exact Sandbox identity. All
// supervision, reply persistence and quiescence checks still run after admission.
// Settled siblings may retain leases for a live activity but do not need intake;
// multiple unsettled commands still use ordinary reconciliation for ordering.
// handled reports that the group was settled here and reconciliation must stop.
func (d *ExecutionDispatcher) sendFreshDelivery(ctx context.Context, group []controlclient.MessageExecution) (handled bool, err error) {
	fresh, index := d.takeFreshDelivery(group)
	if fresh == nil {
		return false, nil
	}
	e := group[index]
	message := fresh.message
	d.ensureOutputCollector(ctx, fresh.route, message.WorkspaceID, message.LogicalSessionID, e.SandboxID)
	request := messageprotocol.Request{Session: messageprotocol.SessionEnvelope{ID: message.LogicalSessionID, WorkspaceID: message.WorkspaceID, KeepAliveSeconds: message.KeepAliveSeconds}, Message: messageprotocol.Message{ID: message.ID, Payload: message.Payload}, Observability: e.Observability, Reply: message.ReplyRequested}
	receipt, err := d.mailbox.Admit(ctx, fresh.route, request, e.ExecutionDeadline)
	if err != nil {
		return false, err
	} // Next pass must query Status before any retry.
	if receipt.Validate(e.MessageID, e.SandboxID) != nil || !receipt.Deadline.Equal(e.ExecutionDeadline) || (e.Observability != nil && receipt.AttemptID != e.Observability.AttemptID) {
		return true, d.terminate(ctx, group, "runtime_protocol_mismatch")
	}
	e.Receipt = &receipt
	switch receipt.State {
	case messageprotocol.ReceiptQueued:
		e.Phase = messageprotocol.PhaseAccepted
	case messageprotocol.ReceiptRunning:
		e.Phase = messageprotocol.PhaseRunning
	case messageprotocol.ReceiptCancelling:
		e.Phase = messageprotocol.PhaseCancelling
	case messageprotocol.ReceiptSucceeded, messageprotocol.ReceiptFailed, messageprotocol.ReceiptTimedOut:
		// Admission is confirmed, but ordinary reply handling must precede
		// settlement. A missing reservation must not trigger another POST.
		e.Phase = messageprotocol.PhaseAccepted
	}
	saved, err := d.control.SaveExecution(ctx, e)
	if err != nil {
		return false, err
	}
	group[index] = saved
	return false, nil
}
