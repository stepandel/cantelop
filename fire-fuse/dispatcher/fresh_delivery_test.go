package dispatcher

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/controlclient"
	"github.com/stepandel/cantelop/fire-fuse/messageprotocol"
	"github.com/stepandel/cantelop/fire-fuse/runtime/session"
)

type freshTestMailbox struct {
	*executionTestMailbox
	calls    []string
	admitErr error
}

func (m *freshTestMailbox) Admit(ctx context.Context, r session.Route, request messageprotocol.Request, deadline time.Time) (messageprotocol.Receipt, error) {
	m.calls = append(m.calls, "admit")
	m.admissions++
	return m.receipt, m.admitErr
}
func (m *freshTestMailbox) Status(ctx context.Context, r session.Route, id string) (messageprotocol.Receipt, error) {
	m.calls = append(m.calls, "status")
	return m.executionTestMailbox.Status(ctx, r, id)
}
func (m *freshTestMailbox) Inspect(ctx context.Context, r session.Route) (messageprotocol.RuntimeStatus, error) {
	m.calls = append(m.calls, "inspect")
	return m.executionTestMailbox.Inspect(ctx, r)
}

// Registers the fixture's pre-save execution, as admission does before its save.
func rememberFixtureFresh(d *ExecutionDispatcher, e controlclient.MessageExecution) {
	e.Revision--
	d.rememberFreshDelivery(e, controlclient.MessageDelivery{ID: e.MessageID, Attempts: 1, WorkspaceID: "workspace", LogicalSessionID: "thread"}, session.Route{SandboxID: e.SandboxID}, d.now())
}
func TestFreshDeliveryPrecedesStatusAndInspection(t *testing.T) {
	d, c, base, e := executionFixture(t)
	m := &freshTestMailbox{executionTestMailbox: base}
	d.mailbox = m
	rememberFixtureFresh(d, e)
	if err := d.reconcile(context.Background(), []controlclient.MessageExecution{e}); err != nil {
		t.Fatal(err)
	}
	if len(m.calls) < 3 || m.calls[0] != "admit" || m.admissions != 1 {
		t.Fatalf("calls=%v", m.calls)
	}
	if c.saved[0].Receipt == nil || c.saved[0].Phase != "running" {
		t.Fatal("first-send receipt was not persisted")
	}
	if m.calls[len(m.calls)-1] != "inspect" {
		t.Fatal("post-admission supervision was skipped")
	}
}

func TestFreshDeliveryWithSettledHistoryPrecedesStatusAndInspection(t *testing.T) {
	d, _, base, e := executionFixture(t)
	m := &freshTestMailbox{executionTestMailbox: base}
	d.mailbox = m
	rememberFixtureFresh(d, e)
	old := e
	old.MessageID = "msg_00000000000000000000000000000000"
	old.Phase = "settled"
	receipt := base.receipt
	receipt.MessageID, receipt.State = old.MessageID, "succeeded"
	old.Receipt = &receipt
	group := []controlclient.MessageExecution{old, e}
	if err := d.reconcile(context.Background(), group); err != nil {
		t.Fatal(err)
	}
	if len(m.calls) == 0 || m.calls[0] != "admit" || m.admissions != 1 {
		t.Fatalf("settled history delayed the new admission: calls=%v", m.calls)
	}
	if group[0].Receipt.State != "succeeded" || group[1].Receipt == nil || group[1].Receipt.MessageID != e.MessageID {
		t.Fatal("new receipt replaced the wrong execution")
	}
}
func TestFreshDeliveryLostACKQueriesStatusWithoutResending(t *testing.T) {
	d, _, base, e := executionFixture(t)
	m := &freshTestMailbox{executionTestMailbox: base, admitErr: errors.New("lost ACK")}
	d.mailbox = m
	rememberFixtureFresh(d, e)
	if err := d.reconcile(context.Background(), []controlclient.MessageExecution{e}); err == nil {
		t.Fatal("lost ACK was ignored")
	}
	m.admitErr = nil
	if err := d.reconcile(context.Background(), []controlclient.MessageExecution{e}); err != nil {
		t.Fatal(err)
	}
	if m.admissions != 1 {
		t.Fatal("ambiguous first send was blindly repeated")
	}
	if len(m.calls) < 3 || m.calls[1] != "inspect" || m.calls[2] != "status" {
		t.Fatalf("recovery calls=%v", m.calls)
	}
}
func TestFreshDeliveryEligibilityFallsBackToReconciliation(t *testing.T) {
	for _, kind := range []string{"expired", "revision", "retry", "restart", "multiple", "deadline", "binding"} {
		t.Run(kind, func(t *testing.T) {
			d, _, base, e := executionFixture(t)
			m := &freshTestMailbox{executionTestMailbox: base}
			d.mailbox = m
			rememberFixtureFresh(d, e)
			group := []controlclient.MessageExecution{e}
			switch kind {
			case "expired":
				now := d.now()
				d.now = func() time.Time { return now.Add(freshDeliveryLifetime) }
			case "revision":
				group[0].Revision++
			case "retry":
				group[0].Attempt++
			case "restart":
				d.fresh.pending = nil
			case "multiple":
				group = append(group, e)
			case "deadline":
				group[0].ExecutionDeadline = group[0].ExecutionDeadline.Add(time.Second)
			case "binding":
				group[0].SandboxID = "sbx-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			}
			if handled, err := d.sendFreshDelivery(context.Background(), group); handled || err != nil {
				t.Fatal(handled, err)
			}
			if m.admissions != 0 {
				t.Fatal("ineligible first-send bypassed reconciliation")
			}
			if len(d.fresh.pending) != 0 {
				t.Fatal("ineligible handoff was retained")
			}
		})
	}
}
func TestFreshDeliveryRequiresFirstDurableRevision(t *testing.T) {
	d, _, _, e := executionFixture(t)
	e.Revision = 2
	rememberFixtureFresh(d, e)
	if len(d.fresh.pending) != 0 {
		t.Fatal("existing durable execution was treated as fresh")
	}
}
func TestFreshTerminalReplyStillPersistsBeforeSettlement(t *testing.T) {
	d, c, m, e := executionFixture(t)
	m.receipt.State = "succeeded"
	c.message = controlclient.MessageDelivery{ID: e.MessageID, WorkspaceID: "workspace", LogicalSessionID: "thread", ReplyRequested: true}
	unsaved := e
	unsaved.Revision--
	d.rememberFreshDelivery(unsaved, c.message, session.Route{SandboxID: e.SandboxID}, d.now())
	if err := d.reconcile(context.Background(), []controlclient.MessageExecution{e}); err != nil {
		t.Fatal(err)
	}
	if len(c.savedReply) == 0 || c.saved[len(c.saved)-1].Phase != "settled" {
		t.Fatal("fresh terminal reply skipped durable settlement")
	}
}

type freshReceiptSaveControl struct {
	*executionTestControl
	fail bool
}

func (c *freshReceiptSaveControl) SaveExecution(ctx context.Context, e controlclient.MessageExecution) (controlclient.MessageExecution, error) {
	if c.fail {
		return e, errors.New("receipt persistence lost")
	}
	return c.executionTestControl.SaveExecution(ctx, e)
}
func TestFreshReceiptPersistenceFailureDoesNotRepeatPOST(t *testing.T) {
	d, baseControl, baseMailbox, e := executionFixture(t)
	c := &freshReceiptSaveControl{executionTestControl: baseControl, fail: true}
	d.control = c
	m := &freshTestMailbox{executionTestMailbox: baseMailbox}
	d.mailbox = m
	rememberFixtureFresh(d, e)
	if err := d.reconcile(context.Background(), []controlclient.MessageExecution{e}); err == nil {
		t.Fatal("receipt persistence failure ignored")
	}
	c.fail = false
	if err := d.reconcile(context.Background(), []controlclient.MessageExecution{e}); err != nil {
		t.Fatal(err)
	}
	if m.admissions != 1 {
		t.Fatal("receipt persistence failure repeated POST")
	}
}
func TestFreshDeliveryInvalidReceiptTerminatesPinnedSandbox(t *testing.T) {
	d, c, m, e := executionFixture(t)
	m.receipt.Deadline = m.receipt.Deadline.Add(time.Second)
	rememberFixtureFresh(d, e)
	if handled, err := d.sendFreshDelivery(context.Background(), []controlclient.MessageExecution{e}); !handled || err != nil {
		t.Fatal(handled, err)
	}
	if len(c.terminated) != 1 || c.terminated[0] != e.SandboxID || c.saved[len(c.saved)-1].ErrorCode != "runtime_protocol_mismatch" {
		t.Fatal("invalid receipt was accepted")
	}
}
func TestSuccessfulAdmissionRegistersFreshHandoffOnlyAfterSave(t *testing.T) {
	d, c, _, e := executionFixture(t)
	c.claimed = &controlclient.MessageDelivery{ID: e.MessageID, WorkspaceID: "workspace", LogicalSessionID: "thread", Attempts: 1, AcceptedAt: d.now()}
	if err := d.admitNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(c.saved) != 1 || func() bool { fresh, _ := d.takeFreshDelivery(c.saved); return fresh == nil }() {
		t.Fatal("successful first revision did not register handoff")
	}
	d2, c2, _, e2 := executionFixture(t)
	c2.claimed = &controlclient.MessageDelivery{ID: e2.MessageID, WorkspaceID: "workspace", LogicalSessionID: "thread", Attempts: 1, AcceptedAt: d2.now()}
	d2.control = &freshReceiptSaveControl{executionTestControl: c2, fail: true}
	if err := d2.admitNext(context.Background()); err == nil {
		t.Fatal("failed ownership persistence ignored")
	}
	if len(d2.fresh.pending) != 0 {
		t.Fatal("failed ownership persistence registered a handoff")
	}
}
func TestFreshDeliveryInvalidReceiptStopsReconciliation(t *testing.T) {
	d, _, base, e := executionFixture(t)
	base.receipt.Deadline = base.receipt.Deadline.Add(time.Second)
	m := &freshTestMailbox{executionTestMailbox: base}
	d.mailbox = m
	rememberFixtureFresh(d, e)
	if err := d.reconcile(context.Background(), []controlclient.MessageExecution{e}); err != nil {
		t.Fatal(err)
	}
	if len(m.calls) != 1 || m.calls[0] != "admit" {
		t.Fatalf("terminated group was reconciled further: calls=%v", m.calls)
	}
}
