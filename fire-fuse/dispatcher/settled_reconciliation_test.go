package dispatcher

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/controlclient"
	"github.com/stepandel/cantelop/fire-fuse/messageprotocol"
	"github.com/stepandel/cantelop/fire-fuse/runtime/session"
)

type settledTestControl struct {
	*executionTestControl
	reads int
}

func (c *settledTestControl) GetMessageDelivery(ctx context.Context, id string) (controlclient.MessageDelivery, error) {
	c.reads++
	return c.executionTestControl.GetMessageDelivery(ctx, id)
}

type settledTestMailbox struct {
	*executionTestMailbox
	statuses, inspections int
	activity              *messageprotocol.ActivityStatus
}

func (m *settledTestMailbox) Status(ctx context.Context, route session.Route, id string) (messageprotocol.Receipt, error) {
	m.statuses++
	receipt, err := m.executionTestMailbox.Status(ctx, route, id)
	receipt.MessageID = id
	return receipt, err
}

func (m *settledTestMailbox) Inspect(ctx context.Context, route session.Route) (messageprotocol.RuntimeStatus, error) {
	m.inspections++
	snapshot, err := m.executionTestMailbox.Inspect(ctx, route)
	snapshot.Activity = m.activity
	return snapshot, err
}

func TestSettledHistoryDoesNotRepollReceiptsOrRewriteUnchangedExecutions(t *testing.T) {
	for _, count := range []int{1, 10, 67, 100} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			d, base, mailbox, e := executionFixture(t)
			control := &settledTestControl{executionTestControl: base}
			runtime := &settledTestMailbox{executionTestMailbox: mailbox,
				activity: &messageprotocol.ActivityStatus{ID: "claude", Deadline: d.now().Add(24 * time.Hour)}}
			d.control, d.mailbox = control, runtime
			runtime.receipt.State = "succeeded"
			group := make([]controlclient.MessageExecution, count)
			now := d.now()
			for i := range group {
				current := e
				current.MessageID = fmt.Sprintf("msg_%032x", i+1)
				current.Phase, current.PhaseAt = "settled", now
				receipt := runtime.receipt
				receipt.MessageID = current.MessageID
				current.Receipt, current.Activity = &receipt, runtime.activity
				current.Inspection = &controlclient.RuntimeInspection{LastSuccessAt: &now}
				group[i] = current
				d.renewed[executionLease(current)] = now
			}
			if err := d.reconcile(context.Background(), group); err != nil {
				t.Fatal(err)
			}
			if control.reads != 1 || len(control.saved) != 0 || runtime.statuses != 0 || runtime.inspections != 2 {
				t.Fatalf("settled history repeated per-Message I/O: reads=%d saves=%d statuses=%d inspections=%d",
					control.reads, len(control.saved), runtime.statuses, runtime.inspections)
			}
			if runtime.admissions != 0 || control.released != 0 || len(control.terminated) != 0 {
				t.Fatal("settled history changed runtime ownership")
			}
		})
	}
}
