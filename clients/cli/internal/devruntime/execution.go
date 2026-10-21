package devruntime

import (
	"context"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/messageprotocol"
)

// Keep the SDK-visible projection in memory, using the remote gateway's shape.
// Receipt and snapshot values are immutable once published under runtime.mu.
type localExecution struct {
	Phase     string                          `json:"phase"`
	PhaseAt   time.Time                       `json:"phase_at"`
	Outcome   string                          `json:"outcome"`
	SandboxID string                          `json:"sandbox_id"`
	Deadline  time.Time                       `json:"deadline"`
	Receipt   *messageprotocol.Receipt        `json:"receipt"`
	Activity  *messageprotocol.ActivityStatus `json:"activity"`
	WorkState string                          `json:"work_state"`
	Reason    string                          `json:"reason"`
}

type localExecutionObserverKey struct{}

type localExecutionObserver func(*messageprotocol.Receipt, *messageprotocol.RuntimeStatus)

func reportLocalReceipt(ctx context.Context, receipt messageprotocol.Receipt) {
	if observe, ok := ctx.Value(localExecutionObserverKey{}).(localExecutionObserver); ok {
		observe(&receipt, nil)
	}
}

func reportLocalRuntime(ctx context.Context, snapshot messageprotocol.RuntimeStatus) {
	if observe, ok := ctx.Value(localExecutionObserverKey{}).(localExecutionObserver); ok {
		observe(nil, &snapshot)
	}
}

func (runtime *localRuntime) observeLocalExecution(ctx context.Context, record *localMessageRecord) context.Context {
	return context.WithValue(ctx, localExecutionObserverKey{}, localExecutionObserver(func(receipt *messageprotocol.Receipt, snapshot *messageprotocol.RuntimeStatus) {
		runtime.mu.Lock()
		defer runtime.mu.Unlock()
		if snapshot != nil {
			record.recoverySupported = snapshot.Capabilities.Recovery
		}
		var value localExecution
		if record.execution != nil {
			value = *record.execution
		}
		phase := value.Phase
		if receipt != nil {
			value.Receipt = receipt
			value.SandboxID = receipt.SandboxID
			value.Deadline = receipt.Deadline
			value.Outcome = "pending"
			value.WorkState = "delivering"
			switch receipt.State {
			case "queued":
				value.Phase = "accepted"
			case "running":
				value.Phase = "running"
			case "cancelling":
				value.Phase = "cancelling"
			case "succeeded", "failed", "timed_out":
				value.Phase = "settled"
				value.Outcome = string(receipt.State)
				value.WorkState = "awaiting_quiescence"
			}
		}
		if snapshot != nil && value.Receipt != nil {
			value.Activity = snapshot.Activity
			if snapshot.Quiescent && snapshot.Generation >= value.Receipt.Generation && snapshot.Events.Acknowledged == snapshot.Events.Latest {
				value.Phase = "quiescent"
				value.WorkState = "quiescent"
			}
		}
		if value.Phase != phase {
			value.PhaseAt = runtime.now().UTC()
		}
		if value.Receipt != nil {
			record.execution = &value
		}
	}))
}

func (runtime *localRuntime) failLocalExecution(record *localMessageRecord, err error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if record.execution == nil {
		return
	}
	value := *record.execution
	value.Phase = "unavailable"
	value.PhaseAt = runtime.now().UTC()
	value.WorkState = "unavailable"
	value.Reason = localFailureCode(err)
	if value.Outcome == "pending" {
		value.Outcome = "unknown"
	}
	record.execution = &value
}
