package dispatcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/controlclient"
	"github.com/stepandel/cantelop/fire-fuse/messagedelivery"
	"github.com/stepandel/cantelop/fire-fuse/messageprotocol"
	"github.com/stepandel/cantelop/fire-fuse/observability"
	"github.com/stepandel/cantelop/fire-fuse/runtime/session"
	"github.com/stepandel/cantelop/fire-fuse/sessionbroker"
)

const (
	// sessionAdmissionWindow bounds how long an accepted Message may wait for
	// a Session binding before admission fails.
	sessionAdmissionWindow = 30 * time.Second
	// runtimeAdmissionWindow bounds how long a bound attempt may wait for its
	// runtime to acknowledge intake. It sets the persisted AdmissionDeadline.
	runtimeAdmissionWindow = 30 * time.Second
	// executionWindow bounds a first attempt from binding to settlement.
	// Redeliveries inherit the original execution deadline.
	executionWindow = 5 * time.Minute
	// supervisionGrace is how long the dispatcher waits past a runtime
	// deadline or cancellation request before terminating the Sandbox.
	supervisionGrace = 10 * time.Second
	// settledHandoffWindow bounds how long settled work without runtime
	// activity may wait for quiescence and acknowledged Session output.
	settledHandoffWindow = 30 * time.Second
)

// ExecutionStore persists execution ownership and lifecycle evidence.
type ExecutionStore interface {
	SaveExecution(context.Context, controlclient.MessageExecution) (controlclient.MessageExecution, error)
	ListExecutions(context.Context) ([]controlclient.MessageExecution, error)
}

// PinnedSandboxes fences supervision to the exact Sandbox that owns an
// execution, never the Session's potentially newer allocation.
type PinnedSandboxes interface {
	GetPinnedRoute(context.Context, string, string) (session.Route, error)
	PinnedWorkLease(context.Context, string, string, string, bool) error
	ReleasePinnedWorkLease(context.Context, string, string, string) error
	TerminateSandbox(context.Context, string) error
}

// DeliveryNotifications hints that new Message deliveries may be claimable.
// Admission still polls; notifications only shorten the wait.
type DeliveryNotifications interface {
	WaitForMessageDeliveries(context.Context, uint64) (uint64, error)
}

type SessionOutputControl interface {
	AppendSessionEvent(context.Context, controlclient.SessionEventAppend) (controlclient.SessionEvent, error)
}

// ExecutionControl is the complete control-plane capability the dispatcher
// composes from its role interfaces.
type ExecutionControl interface {
	controlclient.MessageDeliveries
	controlclient.WorkspaceDirectory
	sessionbroker.Control
	ExecutionStore
	PinnedSandboxes
	DeliveryNotifications
	SessionOutputControl
}
type Mailbox interface {
	Admit(context.Context, session.Route, messageprotocol.Request, time.Time) (messageprotocol.Receipt, error)
	Status(context.Context, session.Route, string) (messageprotocol.Receipt, error)
	Reply(context.Context, session.Route, string) (messageprotocol.MessageReply, error)
	Inspect(context.Context, session.Route) (messageprotocol.RuntimeStatus, error)
	Cancel(context.Context, session.Route, string) error
	CancelActivity(context.Context, session.Route, string) error
	CollectObservations(context.Context, session.Route, func(messageprotocol.ObservationEvent) error) error
	Recover(context.Context, session.Route, messageprotocol.RecoveryRequest) (messageprotocol.RecoveryReceipt, error)
}
type OutputCollector interface {
	Collect(context.Context, session.Route, func(messageprotocol.OutputEvent) error) error
}
type outputCollection struct {
	cancel context.CancelFunc
	done   chan struct{}
	owners map[string]bool
}

// ExecutionDispatcher separates bounded admission from lifecycle reconciliation.
// The deployment runs one dispatcher. Sandbox output collectors run independently
// of reconciliation; durable revisions fence stale lifecycle writes.
type ExecutionDispatcher struct {
	control           ExecutionControl
	broker            *sessionbroker.Broker
	mailbox           Mailbox
	output            OutputCollector
	observe           messagedelivery.ObservationRecorder
	lifecycle         observability.Sink
	traceScopes       sync.Map
	logger            *slog.Logger
	now               func() time.Time
	interval          time.Duration
	reconcileWake     chan struct{}
	completionWake    chan struct{}
	admissionWake     chan struct{}
	fresh             freshDeliveries
	admissionMu       sync.RWMutex
	leaseMu           sync.Mutex
	renewed           map[string]time.Time
	outputMu          sync.Mutex
	outputs           map[string]outputCollection
	reconcileMu       sync.Mutex
	pendingReconciles map[string]bool
}

func NewExecutionDispatcher(control ExecutionControl, mailbox Mailbox, output OutputCollector, observe messagedelivery.ObservationRecorder, lifecycle observability.Sink, logger *slog.Logger) (*ExecutionDispatcher, error) {
	if control == nil || mailbox == nil || output == nil {
		return nil, errors.New("execution control, mailbox, and Session output collector required")
	}
	broker, err := sessionbroker.New(control)
	if err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &ExecutionDispatcher{control: control, broker: broker, mailbox: mailbox, output: output, observe: observe, lifecycle: lifecycle, logger: logger, now: time.Now, interval: time.Second, reconcileWake: make(chan struct{}, 1), completionWake: make(chan struct{}, 1), admissionWake: make(chan struct{}, 4), renewed: make(map[string]time.Time), outputs: make(map[string]outputCollection), pendingReconciles: make(map[string]bool)}, nil
}
func (d *ExecutionDispatcher) Run(ctx context.Context) error {
	defer d.stopOutputCollectors(nil)
	// Only unbound claims can be requeued. Bound attempts must be reconciled.
	if _, err := d.control.RequeueHandlingMessageDeliveries(ctx); err != nil {
		return err
	}
	workers, stop := context.WithCancel(ctx)
	defer stop()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		var generation uint64
		for workers.Err() == nil {
			next, err := d.control.WaitForMessageDeliveries(workers, generation)
			if err != nil {
				// An older or unauthorized control plane lacks the route; admission
				// still polls, so stop hinting instead of warning every interval.
				var response *controlclient.Error
				if errors.As(err, &response) && (response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusForbidden) {
					d.logger.Info("Message notifications unavailable", "status", response.StatusCode)
					return
				}
				if workers.Err() == nil {
					d.logger.Warn("Message notification deferred", "error", err)
				}
				if !waitMessageRetry(workers, d.interval) {
					return
				}
				continue
			}
			if next != generation {
				generation = next
				for i := 0; i < 4; i++ {
					select {
					case d.admissionWake <- struct{}{}:
					default:
					}
				}
			}
		}
	}()
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for workers.Err() == nil {
				admitted, err := d.admitAvailable(workers)
				if err != nil && workers.Err() == nil {
					d.logger.Warn("Message admission deferred", "error", err)
				}
				if err != nil {
					if !waitMessageRetry(workers, d.interval) {
						return
					}
				} else if !admitted && !d.waitForAdmission(workers) {
					return
				}
			}
		}()
	}
	reconciliation := newReconciliationWorkers()
	reconciliation.onAvailable = func() { d.wakePendingReconciliations(reconciliation) }
	defer func() { stop(); wg.Wait(); reconciliation.wg.Wait() }()
	full := true
	nextFull := time.Now()
	for ctx.Err() == nil {
		// Retry abandoned unbound claims after an ambiguous control-plane failure.
		// Exclude active admission workers; the repository additionally excludes every
		// durable sandbox binding, so this cannot replay an admitted message.
		if full && d.admissionMu.TryLock() {
			_, err := d.control.RequeueHandlingMessageDeliveries(ctx)
			d.admissionMu.Unlock()
			if err != nil {
				return err
			}
		}
		executions, err := d.control.ListExecutions(ctx)
		if err != nil {
			return err
		}
		groups := make(map[string][]controlclient.MessageExecution)
		activeSandboxes := make(map[string]bool)
		for _, e := range executions {
			groups[e.SandboxID] = append(groups[e.SandboxID], e)
			activeSandboxes[e.SandboxID] = true
			if e.Recovery != nil && e.Recovery.SandboxID != "" {
				activeSandboxes[e.Recovery.SandboxID] = true
			}
		}
		d.reconcileMu.Lock()
		for id := range d.pendingReconciles {
			// An admission saved after this listing is retained while its
			// Sandbox is busy; the release callback lists it again.
			if _, active := groups[id]; !active && !reconciliation.isRunning(id) {
				delete(d.pendingReconciles, id)
			}
		}
		d.reconcileMu.Unlock()
		d.stopOutputCollectors(activeSandboxes)
		for sandboxID, group := range groups {
			d.startReconciliation(reconciliation, sandboxID, func() {
				if err := d.reconcile(workers, group); err != nil && workers.Err() == nil {
					d.logger.Warn("Message reconciliation deferred", "sandbox_id", group[0].SandboxID, "error", err)
				}
			}, !full)
		}
		if full {
			nextFull = time.Now().Add(d.interval)
		}
		cause := d.waitForReconciliationCause(ctx, nextFull)
		if cause == reconciliationStopped {
			break
		}
		full = cause == reconciliationFull
	}
	return nil
}

// Coalesce admissions without blocking their workers. A wake arriving during a
// reconciliation pass stays queued. Each Sandbox has at most one active worker;
// unrelated Sandboxes can start while an earlier pass is still waiting on I/O.
func (d *ExecutionDispatcher) wakeReconciliation() {
	select {
	case d.reconcileWake <- struct{}{}:
	default:
	}
}

const (
	reconciliationStopped = iota
	reconciliationFull
	reconciliationCompletion
)

func (d *ExecutionDispatcher) waitForReconciliation(ctx context.Context) bool {
	return d.waitForReconciliationCause(ctx, time.Now().Add(d.interval)) != reconciliationStopped
}

func (d *ExecutionDispatcher) waitForReconciliationCause(ctx context.Context, nextFull time.Time) int {
	if ctx.Err() != nil {
		return reconciliationStopped
	}
	if !time.Now().Before(nextFull) {
		return reconciliationFull
	}
	timer := time.NewTimer(time.Until(nextFull))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return reconciliationStopped
	case <-d.reconcileWake:
		return reconciliationFull
	case <-timer.C:
		return reconciliationFull
	case <-d.completionWake:
		// Completion traffic must not postpone periodic supervision indefinitely.
		if !time.Now().Before(nextFull) {
			return reconciliationFull
		}
		return reconciliationCompletion
	}
}

func (d *ExecutionDispatcher) waitForAdmission(ctx context.Context) bool {
	timer := time.NewTimer(d.interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-d.admissionWake:
		return true
	case <-timer.C:
		return true
	}
}

func (d *ExecutionDispatcher) admitNext(ctx context.Context) error {
	_, err := d.admitAvailable(ctx)
	return err
}

func (d *ExecutionDispatcher) admitAvailable(ctx context.Context) (bool, error) {
	d.admissionMu.RLock()
	defer d.admissionMu.RUnlock()
	message, err := d.control.ClaimNextMessageDelivery(ctx)
	if err != nil || message == nil {
		return false, err
	}
	return true, d.admitClaimed(ctx, message)
}

// Caller retains admissionMu from claim through durable execution persistence.
func (d *ExecutionDispatcher) admitClaimed(ctx context.Context, message *controlclient.MessageDelivery) error {
	// The queue persists backoff using attempts and updated_at. Expire retries
	// before another startup; a dispatcher restart cannot reset this deadline.
	retrying := message.Execution != nil && message.Execution.Phase == messageprotocol.PhaseRetrying
	if retrying && (message.Attempts > controlclient.MaxMessageAttempts || !d.now().Before(message.Execution.ExecutionDeadline)) {
		return d.control.FailMessageDelivery(ctx, message.ID, "", "execution_outcome_unknown")
	}
	if !retrying && message.Attempts > 1 && !d.now().Before(message.AcceptedAt.Add(sessionAdmissionWindow)) {
		return d.control.FailMessageDelivery(ctx, message.ID, "", "session_admission_timeout")
	}
	workspace, err := d.control.GetWorkspace(ctx, message.WorkspaceID)
	if err != nil || workspace.ArchivedAt != nil {
		return d.control.FailMessageDelivery(ctx, message.ID, "", "workspace_unavailable")
	}
	active, err := d.broker.OpenLogical(ctx, sessionbroker.LogicalSpec{AppID: workspace.AppID, WorkspaceID: message.WorkspaceID, LogicalID: message.LogicalSessionID, KeepAliveSeconds: message.KeepAliveSeconds})
	if err != nil {
		return d.retryUnbound(ctx, *message, err)
	}
	route, err := d.control.GetSessionRoute(ctx, active.ID)
	if err != nil {
		return d.retryUnbound(ctx, *message, err)
	}
	runtime, err := d.mailbox.Inspect(ctx, route)
	if err != nil {
		return d.retryUnbound(ctx, *message, err)
	}
	if message.ReplyRequested && !runtime.Capabilities.Replies {
		// Refuse before admission: an older runtime must not run the handler
		// for a request whose reply it can never return.
		return d.control.FailMessageDelivery(ctx, message.ID, "", "request_reply_unsupported")
	}
	now := d.now().UTC().Truncate(time.Millisecond)
	trace, err := messageprotocol.NewObservabilityContext(message.RequestID, message.Attempts)
	if err != nil {
		return err
	}
	e := controlclient.MessageExecution{MessageID: message.ID, Attempt: message.Attempts, SessionID: active.ID, SandboxID: route.SandboxID, Phase: messageprotocol.PhaseSending, AdmissionDeadline: now.Add(runtimeAdmissionWindow), ExecutionDeadline: now.Add(executionWindow), Observability: &trace, RecoverySupported: runtime.Capabilities.Recovery, RedeliverySupported: runtime.Capabilities.Redelivery}
	if retrying {
		if !runtime.Capabilities.Redelivery {
			return d.control.FailMessageDelivery(ctx, message.ID, "", "redelivery_unsupported")
		}
		e.ExecutionDeadline = message.Execution.ExecutionDeadline
		if e.AdmissionDeadline.After(e.ExecutionDeadline) {
			e.AdmissionDeadline = e.ExecutionDeadline
		}
	}
	if err = d.control.PinnedWorkLease(ctx, e.SessionID, e.SandboxID, executionLease(e), false); err != nil {
		return d.retryUnbound(ctx, *message, err)
	}
	// Save the intended identity before sending; on ambiguous persistence failure
	// leave the claim and short lease for recovery, never send to an unrecorded owner.
	// Register the handoff first, so any pass that observes the saved row consumes it.
	d.rememberFreshDelivery(e, *message, route, now)
	_, err = d.control.SaveExecution(ctx, e)
	if err != nil {
		d.forgetFreshDelivery(e.MessageID)
		return err
	}
	d.requestReconciliation(e.SandboxID)
	return nil
}
func (d *ExecutionDispatcher) retryUnbound(ctx context.Context, m controlclient.MessageDelivery, cause error) error {
	deadline := m.AcceptedAt.Add(sessionAdmissionWindow)
	if m.Execution != nil && m.Execution.Phase == messageprotocol.PhaseRetrying {
		deadline = m.Execution.ExecutionDeadline
	}
	if !d.now().Before(deadline) {
		code := "session_start_failed"
		var response *controlclient.Error
		if errors.As(cause, &response) && response.StatusCode == http.StatusTooManyRequests {
			code = "capacity_unavailable"
		}
		return d.control.FailMessageDelivery(ctx, m.ID, "", code)
	}
	return errors.Join(cause, d.control.RequeueMessageDelivery(ctx, m.ID))
}
func executionLease(e controlclient.MessageExecution) string {
	sum := sha256.Sum256([]byte(e.SandboxID + ":" + e.MessageID))
	return "lease_" + hex.EncodeToString(sum[:16])
}
func (d *ExecutionDispatcher) renew(ctx context.Context, e controlclient.MessageExecution) error {
	key := executionLease(e)
	d.leaseMu.Lock()
	at := d.renewed[key]
	d.leaseMu.Unlock()
	if d.now().Sub(at) < time.Minute {
		return nil
	}
	if err := d.control.PinnedWorkLease(ctx, e.SessionID, e.SandboxID, key, true); err != nil {
		if err = d.control.PinnedWorkLease(ctx, e.SessionID, e.SandboxID, key, false); err != nil {
			return err
		}
	}
	d.leaseMu.Lock()
	d.renewed[key] = d.now()
	d.leaseMu.Unlock()
	return nil
}
func (d *ExecutionDispatcher) finish(ctx context.Context, e controlclient.MessageExecution, phase messageprotocol.ExecutionPhase, code string) error {
	e.Phase = phase
	e.ErrorCode = code
	// Release is idempotent and uses a sandbox-specific lease ID. Persist the
	// terminal state only after release, so a failed release remains reconcilable.
	err := d.control.ReleasePinnedWorkLease(ctx, e.SessionID, e.SandboxID, executionLease(e))
	if err != nil {
		return err
	}
	if e, err = d.control.SaveExecution(ctx, e); err != nil {
		return err
	}
	d.observeExecutionFinished(ctx, e)
	d.leaseMu.Lock()
	delete(d.renewed, executionLease(e))
	d.leaseMu.Unlock()
	return err
}
func (d *ExecutionDispatcher) lost(ctx context.Context, group []controlclient.MessageExecution, code string) error {
	d.stopOutputCollector(group[0].SandboxID)
	for _, e := range group {
		if err := d.finish(ctx, e, messageprotocol.PhaseUnavailable, code); err != nil {
			return err
		}
	}
	return nil
}

func recoveryIdentity(sandboxID string) string {
	sum := sha256.Sum256([]byte("recovery:" + sandboxID))
	return "rcv_" + hex.EncodeToString(sum[:16])
}

func recoveryLease(recoveryID string) string {
	sum := sha256.Sum256([]byte(recoveryID))
	return "lease_" + hex.EncodeToString(sum[:16])
}

func recoveryEnabled(group []controlclient.MessageExecution) bool {
	for _, e := range group {
		if e.Phase == messageprotocol.PhaseRecovering || e.RecoverySupported {
			return true
		}
	}
	return false
}

func (d *ExecutionDispatcher) recover(ctx context.Context, group []controlclient.MessageExecution) error {
	// Fence ordinary admission from replacement activation until the recovery
	// hook has settled. The hook may start supervised activity; that activity
	// continues after this short critical section under the recovery lease.
	if group[0].Recovery == nil || group[0].Recovery.Generation == 0 {
		d.admissionMu.Lock()
		defer d.admissionMu.Unlock()
	}
	firstMessage, err := d.control.GetMessageDelivery(ctx, group[0].MessageID)
	if err != nil {
		return err
	}
	recoveryID := recoveryIdentity(group[0].SandboxID)
	for i := range group {
		e := group[i]
		if e.Phase != messageprotocol.PhaseRecovering {
			if err = d.control.ReleasePinnedWorkLease(ctx, e.SessionID, e.SandboxID, executionLease(e)); err != nil {
				return err
			}
			e.Phase = messageprotocol.PhaseRecovering
			e.ErrorCode = "sandbox_lost"
			e.Recovery = &messageprotocol.RecoveryState{ID: recoveryID}
			e, err = d.control.SaveExecution(ctx, e)
			if err != nil {
				return err
			}
			group[i] = e
		}
	}

	var route session.Route
	recovery := group[0].Recovery
	if recovery.SandboxID != "" {
		route, err = d.control.GetPinnedRoute(ctx, group[0].SessionID, recovery.SandboxID)
	}
	if recovery.SandboxID == "" || err != nil {
		if err != nil {
			var response *controlclient.Error
			if !errors.As(err, &response) || (response.StatusCode != http.StatusNotFound && response.StatusCode != http.StatusConflict) {
				return err
			}
		}
		workspace, workspaceErr := d.control.GetWorkspace(ctx, firstMessage.WorkspaceID)
		if workspaceErr != nil {
			return workspaceErr
		}
		active, openErr := d.broker.OpenLogical(ctx, sessionbroker.LogicalSpec{
			AppID: workspace.AppID, WorkspaceID: firstMessage.WorkspaceID,
			LogicalID: firstMessage.LogicalSessionID, KeepAliveSeconds: firstMessage.KeepAliveSeconds,
		})
		if openErr == nil {
			route, openErr = d.control.GetSessionRoute(ctx, active.ID)
		}
		if openErr != nil {
			return openErr
		}
		for i := range group {
			e := group[i]
			e.Recovery.SandboxID = route.SandboxID
			e.Recovery.Generation = 0
			e, err = d.control.SaveExecution(ctx, e)
			if err != nil {
				return err
			}
			group[i] = e
		}
		recovery = group[0].Recovery
	}

	leaseID := recoveryLease(recovery.ID)
	if err = d.control.PinnedWorkLease(ctx, group[0].SessionID, route.SandboxID, leaseID, recovery.Generation != 0); err != nil {
		if recovery.Generation == 0 || d.control.PinnedWorkLease(ctx, group[0].SessionID, route.SandboxID, leaseID, false) != nil {
			return err
		}
	}
	d.ensureOutputCollector(ctx, route, firstMessage.WorkspaceID, firstMessage.LogicalSessionID, group[0].SandboxID)
	if recovery.Generation == 0 {
		request := messageprotocol.RecoveryRequest{
			RecoveryID: recovery.ID, MessageID: group[0].MessageID,
			Session: messageprotocol.SessionEnvelope{ID: firstMessage.LogicalSessionID, WorkspaceID: firstMessage.WorkspaceID, KeepAliveSeconds: firstMessage.KeepAliveSeconds},
		}
		receipt, recoveryErr := d.mailbox.Recover(ctx, route, request)
		if recoveryErr != nil {
			return recoveryErr
		}
		if receipt.State == "failed" {
			_ = d.control.ReleasePinnedWorkLease(ctx, group[0].SessionID, route.SandboxID, leaseID)
			return d.lost(ctx, group, "recovery_failed")
		}
		for i := range group {
			e := group[i]
			e.Recovery.Generation = receipt.Generation
			e, err = d.control.SaveExecution(ctx, e)
			if err != nil {
				return err
			}
			group[i] = e
		}
		recovery = group[0].Recovery
	}

	snapshot, err := d.inspectRuntime(ctx, route, group)
	if err != nil {
		return err
	}
	if snapshot.Activity != nil && d.now().After(snapshot.Activity.Deadline) {
		if snapshot.Activity.CancellationRequestedAt == nil {
			return d.mailbox.CancelActivity(ctx, route, snapshot.Activity.ID)
		}
		if d.now().After(snapshot.Activity.CancellationRequestedAt.Add(supervisionGrace)) {
			if err = d.control.TerminateSandbox(ctx, route.SandboxID); err != nil {
				return err
			}
			_ = d.control.ReleasePinnedWorkLease(ctx, group[0].SessionID, route.SandboxID, leaseID)
			return d.lost(ctx, group, "recovery_timed_out")
		}
	}
	if !snapshot.Quiescent || snapshot.Generation < recovery.Generation || snapshot.Events.Acknowledged != snapshot.Events.Latest {
		return nil
	}
	if err = d.control.ReleasePinnedWorkLease(ctx, group[0].SessionID, route.SandboxID, leaseID); err != nil {
		return err
	}
	return d.lost(ctx, group, "sandbox_lost_recovered")
}
func (d *ExecutionDispatcher) terminate(ctx context.Context, group []controlclient.MessageExecution, code string) error {
	// Terminate the exact VM, never the Session's potentially newer allocation.
	if err := d.control.TerminateSandbox(ctx, group[0].SandboxID); err != nil {
		return err
	}
	return d.lost(ctx, group, code)
}
func (d *ExecutionDispatcher) reconcile(ctx context.Context, group []controlclient.MessageExecution) error {
	// Rebuild the trace prerequisites from durable ownership before collecting
	// any SDK events, including the first pass after a dispatcher restart.
	for _, e := range group {
		d.ensureExecutionObserved(ctx, e)
	}
	if handled, err := d.sendFreshDelivery(ctx, group); handled || err != nil {
		return err
	}
	route, err := d.control.GetPinnedRoute(ctx, group[0].SessionID, group[0].SandboxID)
	if err != nil {
		var response *controlclient.Error
		if errors.As(err, &response) && (response.StatusCode == 404 || response.StatusCode == 409) {
			if hasRedeliverableIntake(group) {
				return d.retryLostIntake(ctx, group)
			}
			if recoveryEnabled(group) {
				return d.recover(ctx, group)
			}
			return d.lost(ctx, group, "sandbox_lost")
		}
		return err
	}
	// Prefer the current Message: settled history shares its Session identity
	// and is not read again below.
	first := 0
	for i := range group {
		if group[i].Phase != messageprotocol.PhaseSettled {
			first = i
			break
		}
	}
	firstMessage, err := d.control.GetMessageDelivery(ctx, group[first].MessageID)
	if err != nil {
		return err
	}
	// Output starts as soon as the pinned route is available. Slow or failed
	// receipt reconciliation must not pace an already running Session stream.
	d.ensureOutputCollector(ctx, route, firstMessage.WorkspaceID, firstMessage.LogicalSessionID, group[0].SandboxID)
	// Retain ownership through inspection outages until supervision decides the
	// outcome. A long-running activity must not lose its lease while unreachable.
	for _, e := range group {
		if err := d.renew(ctx, e); err != nil {
			return err
		}
	}
	snapshot, err := d.inspectRuntime(ctx, route, group)
	if err != nil {
		return d.inspectionFailed(ctx, group, err)
	}
	messages := map[string]controlclient.MessageDelivery{group[first].MessageID: firstMessage}
	for i := range group {
		e := group[i]
		// Settlement is durable only after the terminal receipt and any request
		// reply have been saved. Its receipt is immutable. Keep supervising the
		// shared runtime and leases, without polling every old command again.
		if e.Phase == messageprotocol.PhaseSettled {
			continue
		}
		message := firstMessage
		if i != first {
			message, err = d.control.GetMessageDelivery(ctx, e.MessageID)
			if err != nil {
				return err
			}
		}
		messages[e.MessageID] = message
		// Reconcile the existing reservation before retrying any uncertain POST.
		receipt, statusErr := d.mailbox.Status(ctx, route, e.MessageID)
		if statusErr != nil {
			var response *messagedelivery.RuntimeResponseError
			if e.Phase == messageprotocol.PhaseSending && errors.As(statusErr, &response) && response.StatusCode == http.StatusNotFound {
				if d.now().After(e.AdmissionDeadline) {
					return d.terminate(ctx, group, "admission_timeout")
				}
				request := messageprotocol.Request{Session: messageprotocol.SessionEnvelope{ID: message.LogicalSessionID, WorkspaceID: message.WorkspaceID, KeepAliveSeconds: message.KeepAliveSeconds}, Message: messageprotocol.Message{ID: message.ID, Payload: message.Payload}, Observability: e.Observability, Reply: message.ReplyRequested}
				receipt, statusErr = d.mailbox.Admit(ctx, route, request, e.ExecutionDeadline)
			}
			if statusErr != nil {
				deadline := e.ExecutionDeadline
				if e.Phase == messageprotocol.PhaseSending {
					deadline = e.AdmissionDeadline
				}
				if d.now().After(deadline.Add(supervisionGrace)) {
					return d.terminate(ctx, group, "execution_outcome_unknown")
				}
				if e.Phase != messageprotocol.PhaseSending && errors.As(statusErr, &response) && response.StatusCode == http.StatusNotFound {
					return d.terminate(ctx, group, "mailbox_reservation_lost")
				}
				return statusErr
			}
		}
		if receipt.Validate(e.MessageID, e.SandboxID) != nil || !receipt.Deadline.Equal(e.ExecutionDeadline) || (e.Observability != nil && receipt.AttemptID != e.Observability.AttemptID) {
			return d.terminate(ctx, group, "runtime_protocol_mismatch")
		}
		e.Receipt = &receipt
		e.RecoverySupported = e.RecoverySupported || snapshot.Capabilities.Recovery
		e.Activity = snapshot.Activity
		e.MessageWork = snapshot.MessageWork
		replyPending := false
		if receipt.State == messageprotocol.ReceiptSucceeded && message.ReplyRequested && len(message.Reply) == 0 {
			if !snapshot.Capabilities.Replies {
				return d.terminate(ctx, group, "request_reply_unsupported")
			}
			reply, replyErr := d.mailbox.Reply(ctx, route, e.MessageID)
			if replyErr == nil {
				replyErr = d.control.SaveMessageReply(ctx, e.MessageID, reply.Reply)
			}
			if replyErr != nil {
				// Stay unsettled so the execution deadline below bounds a reply
				// that never becomes durable, without stalling the rest of the group.
				d.logger.Warn("Message reply deferred", "message_id", e.MessageID, "sandbox_id", e.SandboxID, "error", replyErr)
				replyPending = true
			} else {
				message.Reply = reply.Reply
				messages[e.MessageID] = message
			}
		}
		if e.Phase != messageprotocol.PhaseSettled {
			switch receipt.State {
			case messageprotocol.ReceiptQueued:
				e.Phase = messageprotocol.PhaseAccepted
			case messageprotocol.ReceiptRunning:
				e.Phase = messageprotocol.PhaseRunning
			case messageprotocol.ReceiptCancelling:
				e.Phase = messageprotocol.PhaseCancelling
			case messageprotocol.ReceiptSucceeded, messageprotocol.ReceiptFailed, messageprotocol.ReceiptTimedOut:
				if !replyPending {
					e.Phase = messageprotocol.PhaseSettled
				}
			}
			if d.now().After(e.ExecutionDeadline) && e.Phase != messageprotocol.PhaseSettled {
				if d.now().After(e.ExecutionDeadline.Add(supervisionGrace)) {
					return d.terminate(ctx, group, "message_timed_out")
				}
				if err = d.mailbox.Cancel(ctx, route, e.MessageID); err != nil {
					return err
				}
			}
		}

		previous := group[i].Phase
		e, err = d.control.SaveExecution(ctx, e)
		if err != nil {
			return err
		}
		group[i] = e
		if previous != e.Phase {
			d.logger.Info("Mailbox lifecycle", "message_id", e.MessageID, "sandbox_id", e.SandboxID, "phase", e.Phase, "deadline", e.ExecutionDeadline)
		}
		if err = d.renew(ctx, e); err != nil {
			return err
		}
	}
	// Observations remain snapshots because telemetry is not on the user output path.
	observeRuntimeEvent := func(event messageprotocol.ObservationEvent) error {
		if d.observe == nil {
			return nil
		}
		e := group[0]
		matched := false
		for _, candidate := range group {
			if candidate.MessageID == event.MessageID {
				e = candidate
				matched = true
				break
			}
		}
		if event.MessageID != "" && (!matched || e.Observability == nil || !d.ensureExecutionObserved(ctx, e)) {
			// A stale runtime event must not inherit another Message's identity.
			// Drop telemetry without holding up user execution or stream progress.
			d.logger.Warn("Dropping runtime observation without an observed attempt", "message_id", event.MessageID, "sandbox_id", e.SandboxID)
			return nil
		}
		message, ok := messages[e.MessageID]
		if !ok {
			// Settled commands can still own observations from a live activity.
			// Resolve their identity only when an observation actually arrives.
			var err error
			message, err = d.control.GetMessageDelivery(ctx, e.MessageID)
			if err != nil {
				return err
			}
			messages[e.MessageID] = message
		}
		workspace, err := d.control.GetWorkspace(ctx, message.WorkspaceID)
		if err != nil {
			return err
		}
		record := messagedelivery.ObservationRecord{AppID: workspace.AppID, WorkspaceID: message.WorkspaceID, SessionID: message.LogicalSessionID, ConcreteSessionID: e.SessionID, SandboxID: e.SandboxID, MessageID: event.MessageID, Observation: event.Observation}
		if matched && e.Observability != nil && event.MessageID != "" {
			record.AttemptID = e.Observability.AttemptID
			record.TraceID = e.Observability.TraceParent[3:35]
		}
		err = d.observe(ctx, record)
		if err != nil {
			d.logger.Warn("Runtime observations deferred", "sandbox_id", e.SandboxID, "error", err)
		}
		return err
	}
	observationErr := d.mailbox.CollectObservations(ctx, route, observeRuntimeEvent)
	if observationErr != nil {
		d.logger.Warn("Runtime observations deferred", "sandbox_id", route.SandboxID, "error", observationErr)
	}
	snapshot, err = d.inspectRuntime(ctx, route, group)
	if err != nil {
		return d.inspectionFailed(ctx, group, err)
	}
	if snapshot.MessageWork != nil {
		if d.now().After(snapshot.MessageWork.Deadline.Add(supervisionGrace)) {
			return d.terminate(ctx, group, "message_timed_out")
		}
	}
	if activity := snapshot.Activity; activity != nil {
		overdue := func() bool {
			return d.now().After(activity.Deadline.Add(supervisionGrace)) ||
				(activity.CancellationRequestedAt != nil && d.now().After(activity.CancellationRequestedAt.Add(supervisionGrace)))
		}
		if overdue() {
			return d.terminate(ctx, group, "activity_timed_out")
		}
		if d.now().After(activity.Deadline) {
			if err = d.mailbox.CancelActivity(ctx, route, activity.ID); err != nil {
				return err
			}
		}
		// Cancellation can take long enough to cross the grace deadline.
		if overdue() {
			return d.terminate(ctx, group, "activity_timed_out")
		}
	}
	if snapshot.Quiescent && snapshot.Events.Acknowledged != snapshot.Events.Latest {
		for _, e := range group {
			if e.Phase == messageprotocol.PhaseSettled && d.now().After(e.PhaseAt.Add(settledHandoffWindow)) {
				return d.terminate(ctx, group, "output_handoff_unavailable")
			}
		}
	}
	if snapshot.Quiescent && snapshot.Events.Acknowledged == snapshot.Events.Latest {
		// Activity settlement can emit its final observation after the first
		// collection but before this quiescence snapshot. Collect again while
		// the originating attempts are still available for correlation. Keep
		// this bounded and best effort so telemetry cannot prevent release.
		if snapshot.Observations.Acknowledged < snapshot.Observations.Latest {
			if err := d.mailbox.CollectObservations(ctx, route, observeRuntimeEvent); err != nil {
				d.logger.Warn("Final runtime observations deferred", "sandbox_id", route.SandboxID, "error", err)
			}
		}
		for _, e := range group {
			if e.Phase == messageprotocol.PhaseSettled && snapshot.Generation < e.Receipt.Generation {
				// Settled receipts are not polled each pass. A quiescent runtime
				// behind a receipt may have lost it, as after an in-place restart.
				if _, statusErr := d.mailbox.Status(ctx, route, e.MessageID); statusErr != nil {
					var response *messagedelivery.RuntimeResponseError
					if errors.As(statusErr, &response) && response.StatusCode == http.StatusNotFound {
						return d.terminate(ctx, group, "mailbox_reservation_lost")
					}
					return statusErr
				}
			}
		}
		for _, e := range group {
			if e.Phase == messageprotocol.PhaseSettled && snapshot.Generation >= e.Receipt.Generation {
				if err = d.finish(ctx, e, messageprotocol.PhaseQuiescent, ""); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func supervisionDeadline(e controlclient.MessageExecution) time.Time {
	if e.Phase == messageprotocol.PhaseSending {
		return e.AdmissionDeadline
	}
	if e.Phase != messageprotocol.PhaseSettled {
		return e.ExecutionDeadline
	}
	deadline := e.PhaseAt.Add(settledHandoffWindow)
	if e.Activity != nil {
		deadline = e.Activity.Deadline
	}
	if e.MessageWork != nil && (e.Activity == nil || e.MessageWork.Deadline.Before(deadline)) {
		deadline = e.MessageWork.Deadline
	}
	return deadline
}

// bindingStartedAt recovers when admission bound the attempt to its Sandbox.
// Executions do not persist that instant; AdmissionDeadline is derived from it
// once, at binding, and stays stable across reconciliation. A redelivery whose
// admission deadline was clamped to its execution deadline reports the
// correspondingly earlier start.
func bindingStartedAt(e controlclient.MessageExecution) time.Time {
	return e.AdmissionDeadline.Add(-runtimeAdmissionWindow)
}
