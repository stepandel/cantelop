package dispatcher

import (
	"context"

	"github.com/stepandel/cantelop/fire-fuse/controlclient"
	"github.com/stepandel/cantelop/fire-fuse/messageprotocol"
)

func hasRedeliverableIntake(group []controlclient.MessageExecution) bool {
	for _, e := range group {
		if e.RedeliverySupported && e.Phase != messageprotocol.PhaseSettled && e.Phase != messageprotocol.PhaseRecovering &&
			(e.Receipt == nil || !e.Receipt.State.Unsuccessful()) {
			return true
		}
	}
	return false
}

// A missing route alone does not prove death. Terminate the exact old incarnation
// before permitting redelivery; persistence independently checks its terminal state.
func (d *ExecutionDispatcher) retryLostIntake(ctx context.Context, group []controlclient.MessageExecution) error {
	if err := d.control.TerminateSandbox(ctx, group[0].SandboxID); err != nil {
		return err
	}
	d.stopOutputCollector(group[0].SandboxID)
	for _, e := range group {
		phase := messageprotocol.PhaseUnavailable
		if hasRedeliverableIntake([]controlclient.MessageExecution{e}) && e.Attempt < controlclient.MaxMessageAttempts && d.now().Before(e.ExecutionDeadline) {
			phase = messageprotocol.PhaseRetrying
		}
		if err := d.finish(ctx, e, phase, "sandbox_lost"); err != nil {
			return err
		}
	}
	return nil
}
