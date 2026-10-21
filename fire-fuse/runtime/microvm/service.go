package microvm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/telemetry/warmuptrace"
	"github.com/stepandel/cantelop/fire-fuse/workspacesync"
)

var (
	ErrInvalidSpec           = errors.New("invalid microVM specification")
	ErrUnknownApp            = errors.New("unknown app")
	ErrUnknownWorkspace      = errors.New("unknown workspace")
	ErrUnknownProfile        = errors.New("unknown microVM profile")
	ErrUnknownImage          = errors.New("unknown container image")
	ErrUnsupportedShape      = errors.New("unsupported microVM shape")
	ErrSupervisorUnavailable = errors.New("microVM supervisor is unavailable")
	ErrRuntimeUnavailable    = errors.New("microVM runtime is unavailable")
	ErrStateStore            = errors.New("microVM state store failure")
)

// CreateSpec identifies the App and Workspace that own a new microVM. Sandbox shape is
// inherited from the App's platform-owned sandbox profile.
type CreateSpec struct {
	AppID       string
	WorkspaceID string
}

// Service coordinates lifecycle decisions around the state store.
type Service struct {
	managedHostID   string
	managedOnly     bool
	managedHealth   func() error
	managedStorage  func(string, int64) error
	managedMu       sync.Mutex
	managedTimers   map[string]*time.Timer
	syncFiles       *workspacesync.Store
	store           Store
	supervisor      Supervisor
	admission       *admissionController
	now             func() time.Time
	resourceMu      sync.RWMutex
	sessionLocks    sessionLocks
	deadlineMu      sync.Mutex
	deadlineTimers  map[string]*time.Timer
	workLeaseMu     sync.Mutex
	workLeaseTimers map[string]*time.Timer
}

// NewService builds a lifecycle service using the system clock.
func NewService(store Store) *Service {
	return newService(store, nil, time.Now)
}

// NewServiceWithSupervisor builds a lifecycle service that can manage runtimes.
func NewServiceWithSupervisor(store Store, supervisor Supervisor) *Service {
	return newService(store, supervisor, time.Now)
}

// NewServiceWithSupervisorAndLimits builds a runtime service that enforces
// host-wide admission limits.
func NewServiceWithSupervisorAndLimits(
	store Store,
	supervisor Supervisor,
	limits AdmissionLimits,
) (*Service, error) {
	admission, err := newAdmissionController(limits)
	if err != nil {
		return nil, err
	}
	service := newService(store, supervisor, time.Now)
	service.admission = admission
	if reader, ok := supervisor.(ProfileCapacityReader); ok {
		admission.profileCapacity = reader.ProfileCapacity
	}
	return service, nil
}

func newService(store Store, supervisor Supervisor, now func() time.Time) *Service {
	return &Service{
		store:           store,
		managedTimers:   make(map[string]*time.Timer),
		supervisor:      supervisor,
		now:             now,
		deadlineTimers:  make(map[string]*time.Timer),
		workLeaseTimers: make(map[string]*time.Timer),
	}
}

// Create records a validated microVM in the starting state.
//
// Recording starting does not mean Firecracker is running yet. A later step
// will hand this machine to a process supervisor and transition it to running.
func (s *Service) Create(id string, spec CreateSpec) (Sandbox, error) {
	return s.create(context.Background(), id, spec)
}

func (s *Service) create(ctx context.Context, id string, spec CreateSpec) (Sandbox, error) {
	s.resourceMu.RLock()
	defer s.resourceMu.RUnlock()
	finishValidation := warmuptrace.Start(ctx, "service.validate_create_spec")
	if err := ValidateSandboxID(id); err != nil {
		finishValidation()
		return Sandbox{}, fmt.Errorf("%w: invalid sandbox ID", ErrInvalidSpec)
	}
	if err := validateCreateSpec(spec); err != nil {
		finishValidation()
		return Sandbox{}, err
	}
	finishValidation()

	warmuptrace.SetMachineID(ctx, id)
	assignment, err := s.resolveCreateSpec(spec)
	if err != nil {
		return Sandbox{}, err
	}
	now := s.now().UTC()
	machine := Sandbox{
		ID:            id,
		AppID:         spec.AppID,
		WorkspaceID:   assignment.WorkspaceID,
		Profile:       assignment.Profile,
		BuildID:       assignment.BuildID,
		VCPUs:         assignment.VCPUs,
		MemoryMiB:     assignment.MemoryMiB,
		State:         StateStarting,
		RuntimeTiming: RuntimeTiming{Status: RuntimeTimingPending},
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	releaseManaged, err := s.admitManaged(machine)
	if err != nil {
		return Sandbox{}, err
	}
	defer releaseManaged()
	if s.admission != nil {
		if err := s.admission.create(ctx, s.store, machine); err != nil {
			return Sandbox{}, err
		}
		return machine, nil
	}
	finishStoreCreate := warmuptrace.Start(ctx, "store.create_machine")
	if err := s.store.Create(machine); err != nil {
		finishStoreCreate()
		return Sandbox{}, wrapStateStoreError("record microVM", err)
	}
	finishStoreCreate()

	return machine, nil
}

// CreateAndStart records a microVM and starts its runtime.
func (s *Service) CreateAndStart(ctx context.Context, id string, spec CreateSpec) (Sandbox, error) {
	return s.CreateAndStartWithID(ctx, id, spec)
}

// CreateAndStartWithID creates at most one Sandbox with id. Replaying the same
// specification resumes or returns it; a changed request cannot reuse the ID.
func (s *Service) CreateAndStartWithID(ctx context.Context, id string, spec CreateSpec) (Sandbox, error) {
	s.resourceMu.RLock()
	defer s.resourceMu.RUnlock()
	finishValidation := warmuptrace.Start(ctx, "service.validate_sandbox_create_spec")
	if err := ValidateSandboxID(id); err != nil {
		finishValidation()
		return Sandbox{}, fmt.Errorf("%w: invalid sandbox ID", ErrInvalidSpec)
	}
	if err := validateCreateSpec(spec); err != nil {
		finishValidation()
		return Sandbox{}, err
	}
	finishValidation()
	if s.supervisor == nil {
		return Sandbox{}, ErrSupervisorUnavailable
	}
	existing, err := s.store.Get(id)
	switch {
	case err == nil:
		if existing.AppID != spec.AppID || existing.WorkspaceID != spec.WorkspaceID {
			return Sandbox{}, ErrSandboxConflict
		}
		if _, _, err := s.resolveCreateWorkspace(spec); err != nil {
			return Sandbox{}, err
		}
		warmuptrace.SetMachineID(ctx, existing.ID)
		return s.resumeSandboxCreate(ctx, existing)
	case !errors.Is(err, ErrNotFound):
		return Sandbox{}, wrapStateStoreError("look up sandbox creation", err)
	}
	assignment, err := s.resolveCreateSpec(spec)
	if err != nil {
		return Sandbox{}, err
	}
	finishSupervisorValidation := warmuptrace.Start(ctx, "supervisor.validate_spec")
	if err := s.supervisor.Validate(assignment); err != nil {
		finishSupervisorValidation()
		return Sandbox{}, classifyRuntimeStartError(err)
	}
	finishSupervisorValidation()

	machine, err := s.createOrGet(ctx, id, spec, assignment)
	if err != nil {
		return Sandbox{}, err
	}
	warmuptrace.SetMachineID(ctx, machine.ID)
	return s.resumeSandboxCreate(ctx, machine)
}

func (s *Service) createOrGet(
	ctx context.Context,
	id string,
	spec CreateSpec,
	assignment RuntimeSpec,
) (Sandbox, error) {
	now := s.now().UTC()
	candidate := Sandbox{
		ID: id, AppID: spec.AppID, WorkspaceID: assignment.WorkspaceID, Profile: assignment.Profile,
		BuildID: assignment.BuildID, VCPUs: assignment.VCPUs, MemoryMiB: assignment.MemoryMiB,
		RuntimeTiming: RuntimeTiming{Status: RuntimeTimingPending},
		State:         StateStarting, CreatedAt: now, UpdatedAt: now,
	}
	existing, existingErr := s.store.Get(candidate.ID)
	if existingErr == nil {
		if !sameSandboxCreateRequest(existing, candidate) {
			return Sandbox{}, ErrSandboxConflict
		}
		return existing, nil
	}
	if !errors.Is(existingErr, ErrNotFound) {
		return Sandbox{}, wrapStateStoreError("look up Sandbox", existingErr)
	}
	releaseManaged, err := s.admitManaged(candidate)
	if err != nil {
		return Sandbox{}, err
	}
	defer releaseManaged()

	if s.admission != nil {
		machine, _, err := s.admission.createOrGet(ctx, s.store, candidate)
		if err != nil {
			return Sandbox{}, err
		}
		return machine, nil
	}
	machine, _, err := s.store.CreateOrGet(candidate)
	if errors.Is(err, ErrSandboxConflict) {
		return Sandbox{}, err
	}
	if err != nil {
		return Sandbox{}, wrapStateStoreError("record sandbox", err)
	}
	return machine, nil
}

func (s *Service) resolveCreateSpec(spec CreateSpec) (RuntimeSpec, error) {
	app, workspace, err := s.resolveCreateWorkspace(spec)
	if err != nil {
		return RuntimeSpec{}, err
	}
	sessionRuntime, err := s.store.GetAppSessionRuntime(app.ID)
	if err != nil {
		return RuntimeSpec{}, wrapStateStoreError("resolve Sandbox App Session runtime", err)
	}
	profile, err := ResolveSandboxProfile(sessionRuntime.Runtime.Profile)
	if err != nil {
		return RuntimeSpec{}, errors.Join(ErrInvalidAppSessionRuntime, err)
	}
	return RuntimeSpec{
		WorkspaceID: workspace.ID,
		Profile:     profile.Name,
		BuildID:     sessionRuntime.ActiveBuildID,
		VCPUs:       profile.VCPUs,
		MemoryMiB:   profile.MemoryMiB,
	}, nil
}

func (s *Service) resolveCreateWorkspace(spec CreateSpec) (App, Workspace, error) {
	app, err := s.store.GetAppRecord(spec.AppID)
	if errors.Is(err, ErrAppNotFound) {
		return App{}, Workspace{}, ErrUnknownApp
	}
	if err != nil {
		return App{}, Workspace{}, wrapStateStoreError("resolve sandbox app", err)
	}
	workspace, err := s.store.GetWorkspace(spec.WorkspaceID)
	if errors.Is(err, ErrWorkspaceNotFound) || (err == nil && workspace.AppID != app.ID) {
		return App{}, Workspace{}, ErrUnknownWorkspace
	}
	if err != nil {
		return App{}, Workspace{}, wrapStateStoreError("resolve sandbox Workspace", err)
	}
	if workspace.ArchivedAt != nil {
		return App{}, Workspace{}, fmt.Errorf("%w: %s", ErrWorkspaceUnavailable, workspace.ID)
	}
	return app, workspace, nil
}

func (s *Service) resumeSandboxCreate(ctx context.Context, machine Sandbox) (Sandbox, error) {
	switch machine.State {
	case StateStarting:
		return s.Start(ctx, machine.ID)
	case StateRunning, StateStopping, StateTerminated:
		return machine, nil
	case StateFailed:
		return machine, ErrRuntimeUnavailable
	default:
		return Sandbox{}, fmt.Errorf("%w: cannot resume create from %s", ErrInvalidTransition, machine.State)
	}
}

// Get returns one microVM by ID.
func (s *Service) Get(id string) (Sandbox, error) {
	machine, err := s.store.Get(id)
	if err != nil {
		return Sandbox{}, err
	}
	return s.reconcileRuntime(machine)
}

// List returns all known microVMs in the store's stable order.
func (s *Service) List() ([]Sandbox, error) {
	machines, err := s.store.List()
	if err != nil {
		return nil, err
	}
	for i, machine := range machines {
		machines[i], err = s.reconcileRuntime(machine)
		if err != nil {
			return nil, err
		}
	}
	return machines, nil
}

// ListUsage returns best-effort live readings for running sandboxes. A
// sandbox that exits between the lifecycle read and cgroup read is omitted.
// Samples are never written to the state store.
func (s *Service) ListUsage() ([]RuntimeUsage, error) {
	reader, ok := s.supervisor.(RuntimeUsageReader)
	if !ok {
		return nil, ErrSupervisorUnavailable
	}
	machines, err := s.store.ListActive()
	if err != nil {
		return nil, err
	}
	for i, machine := range machines {
		machines[i], err = s.reconcileRuntime(machine)
		if err != nil {
			return nil, err
		}
	}
	usage := make([]RuntimeUsage, 0, len(machines))
	for _, machine := range machines {
		if machine.State != StateRunning {
			continue
		}
		reading, err := reader.RuntimeUsage(machine.ID)
		if err != nil {
			continue
		}
		reading.SandboxID = machine.ID
		usage = append(usage, reading)
	}
	return usage, nil
}

// HostCapacity returns the live fixed warm-slot pool state. Hosts without a
// capacity-aware supervisor report the same unavailable boundary as usage.
func (s *Service) HostCapacity() (HostCapacity, error) {
	reader, ok := s.supervisor.(HostCapacityReader)
	if !ok {
		return HostCapacity{}, ErrSupervisorUnavailable
	}
	return reader.HostCapacity(), nil
}

// Start asks the supervisor to start a recorded microVM and persists the result.
func (s *Service) Start(ctx context.Context, id string) (Sandbox, error) {
	finishInitialGet := warmuptrace.Start(ctx, "store.get_machine_before_start")
	machine, err := s.store.Get(id)
	finishInitialGet()
	if err != nil {
		return Sandbox{}, wrapStateStoreError("retrieve microVM for start", err)
	}
	if machine.State == StateRunning {
		return machine, nil
	}
	if machine.State != StateStarting {
		return Sandbox{}, fmt.Errorf("%w: cannot start from %s", ErrInvalidTransition, machine.State)
	}
	if s.supervisor == nil {
		return Sandbox{}, ErrSupervisorUnavailable
	}
	if s.admission != nil {
		finishAdmissionWait := warmuptrace.Start(ctx, "admission.wait_for_start_slot")
		release, acquireErr := s.admission.acquireStart(ctx)
		finishAdmissionWait()
		if acquireErr != nil {
			s.recordNotStarted(id)
			return s.failStart(id, acquireErr)
		}
		defer release()

		finishPostAdmissionGet := warmuptrace.Start(ctx, "store.get_machine_after_admission")
		machine, err = s.store.Get(id)
		finishPostAdmissionGet()
		if err != nil {
			return Sandbox{}, wrapStateStoreError("retrieve microVM after admission wait", err)
		}
		if machine.State == StateRunning {
			return machine, nil
		}
		if machine.State != StateStarting {
			return Sandbox{}, fmt.Errorf("%w: cannot start from %s", ErrInvalidTransition, machine.State)
		}
	}

	if err := s.checkManaged(machine); err != nil {
		return s.failStart(id, err)
	}
	checkpoint := s.runtimeCheckpoint(id)
	finishSupervisorStart := warmuptrace.Start(ctx, "supervisor.start")
	ctx = WithRuntimeTimingRecorder(ctx, func(timing RuntimeTiming) error {
		_, err := s.store.RecordRuntimeTiming(id, timing)
		if err != nil {
			return wrapStateStoreError("record runtime timing", err)
		}
		return nil
	})
	runtime, err := s.supervisor.Start(ctx, machine, checkpoint)
	finishSupervisorStart()
	if err != nil {
		return s.failStart(id, err)
	}

	if err := s.checkManaged(machine); err != nil {
		_ = s.supervisor.Stop(context.Background(), machine, checkpoint)
		return s.failStart(id, err)
	}
	finishActivate := warmuptrace.Start(ctx, "store.activate_machine")
	running, err := s.store.Activate(id, runtime, s.now().UTC())
	finishActivate()
	if err == nil {
		s.observeRuntimeExit(id)
		return running, nil
	}

	cleanupErr := s.supervisor.Stop(ctx, machine, checkpoint)
	return Sandbox{}, errors.Join(
		wrapStateStoreError("mark microVM running", err),
		wrapCleanupError(cleanupErr),
	)
}

// recordNotStarted marks verified-zero usage for a record that never reached
// process creation. Timing that has measured a process is left untouched, and
// a write failure is logged but not retried: the record then stays pending,
// which readers already treat as unknown rather than zero.
func (s *Service) recordNotStarted(id string) {
	machine, err := s.store.Get(id)
	if err != nil || machine.RuntimeTiming.Status != RuntimeTimingPending {
		return
	}
	if _, err := s.store.RecordRuntimeTiming(id, RuntimeTiming{Status: RuntimeTimingNotStarted}); err != nil {
		slog.Warn("record not-started runtime timing", "sandbox_id", id, "error", err)
	}
}

func (s *Service) failStart(id string, cause error) (Sandbox, error) {
	startErr := classifyRuntimeStartError(cause)
	failed, transitionErr := s.store.Fail(id, Failure{Reason: FailureRuntimeStart}, s.now().UTC())
	if transitionErr != nil {
		return Sandbox{}, errors.Join(
			fmt.Errorf("start microVM runtime: %w", startErr),
			wrapStateStoreError("mark microVM failed", transitionErr),
		)
	}
	return failed, fmt.Errorf("start microVM runtime: %w", startErr)
}

func (s *Service) runtimeCheckpoint(id string) RuntimeCheckpoint {
	return func(ownership RuntimeOwnership) error {
		if ownership.HostSlotID != "" {
			machine, err := s.store.Get(id)
			if err != nil {
				return err
			}
			if machine.Runtime == (RuntimeOwnership{}) {
				hostSlots, ok := s.store.(HostSlotStore)
				if !ok {
					return errors.New("claim host slot: state store does not support host slots")
				}
				_, err := hostSlots.ClaimHostSlot(
					id,
					ownership.HostSlotID,
					ownership,
					s.now().UTC(),
				)
				return err
			}
		}
		_, err := s.store.CheckpointRuntime(id, ownership, s.now().UTC())
		if err != nil {
			return wrapStateStoreError("checkpoint microVM runtime ownership", err)
		}
		return nil
	}
}

func (s *Service) observeRuntimeExit(id string) {
	done, exists := s.supervisor.RuntimeDone(id)
	if !exists {
		return
	}
	go func() {
		<-done
		machine, err := s.store.Get(id)
		if err != nil {
			return
		}
		// A read will retry reconciliation if this best-effort background write
		// loses a race or the store is temporarily unavailable.
		_, _ = s.reconcileRuntime(machine)
	}()
}

// Terminate moves a microVM through stopping to terminated.
//
// The operation is idempotent. A runtime cleanup failure leaves the machine in
// stopping so a later request can retry it.
func (s *Service) Terminate(ctx context.Context, id string) (Sandbox, error) {
	for {
		machine, err := s.store.Get(id)
		if err != nil {
			return Sandbox{}, wrapStateStoreError("retrieve microVM for termination", err)
		}

		switch machine.State {
		case StateStarting, StateRunning, StateFailed:
			machine, err = s.store.Transition(id, StateStopping, s.now().UTC())
			if errors.Is(err, ErrInvalidTransition) {
				continue
			}
			if err != nil {
				return Sandbox{}, wrapStateStoreError("mark microVM stopping", err)
			}
			continue
		case StateStopping:
			if s.supervisor != nil {
				if err := s.supervisor.Stop(ctx, machine, s.runtimeCheckpoint(id)); err != nil {
					if errors.Is(err, ErrStateStore) {
						return Sandbox{}, err
					}
					return machine, errors.Join(
						ErrRuntimeUnavailable,
						fmt.Errorf("stop microVM runtime: %w", err),
					)
				}
			}
			// Stop has returned, so no start is in flight: timing still pending
			// proves no process was ever created for this record.
			s.recordNotStarted(id)

			machine, err = s.store.Transition(id, StateTerminated, s.now().UTC())
			if errors.Is(err, ErrInvalidTransition) {
				continue
			}
			if err != nil {
				return Sandbox{}, wrapStateStoreError("mark microVM terminated", err)
			}
			return machine, nil
		case StateTerminated:
			return machine, nil
		default:
			return Sandbox{}, fmt.Errorf("%w: cannot terminate from %s", ErrInvalidTransition, machine.State)
		}
	}
}

// Crash injects an abrupt runtime-process failure into a running Sandbox. It
// intentionally leaves cleanup to the normal failed-Sandbox lifecycle path.
func (s *Service) Crash(ctx context.Context, id string) (Sandbox, error) {
	machine, err := s.store.Get(id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Sandbox{}, err
		}
		return Sandbox{}, wrapStateStoreError("retrieve microVM for crash", err)
	}
	if machine.State != StateRunning {
		return Sandbox{}, fmt.Errorf("%w: cannot crash from %s", ErrInvalidTransition, machine.State)
	}
	crasher, ok := s.supervisor.(RuntimeCrasher)
	if !ok {
		return Sandbox{}, ErrSupervisorUnavailable
	}
	if err := crasher.Crash(ctx, id); err != nil {
		if errors.Is(err, ErrInvalidTransition) {
			return Sandbox{}, err
		}
		return Sandbox{}, errors.Join(ErrRuntimeUnavailable, err)
	}
	return s.reconcileRuntime(machine)
}

// TerminateAll concurrently terminates every non-terminated microVM.
func (s *Service) TerminateAll(ctx context.Context) error {
	machines, err := s.store.ListActive()
	if err != nil {
		return wrapStateStoreError("list microVMs for termination", err)
	}

	errorsByMachine := make(chan error, len(machines))
	var pending sync.WaitGroup
	for _, machine := range machines {
		pending.Add(1)
		go func(id string) {
			defer pending.Done()
			if _, err := s.Terminate(ctx, id); err != nil {
				errorsByMachine <- fmt.Errorf("terminate %s: %w", id, err)
			}
		}(machine.ID)
	}

	pending.Wait()
	close(errorsByMachine)

	var terminationErrors []error
	for err := range errorsByMachine {
		terminationErrors = append(terminationErrors, err)
	}
	return errors.Join(terminationErrors...)
}

func (s *Service) reconcileRuntime(machine Sandbox) (Sandbox, error) {
	if machine.State != StateRunning || s.supervisor == nil {
		return machine, nil
	}
	// Best effort: a failed retry is repeated by the next lifecycle checkpoint
	// or read and must not turn reads or exit reconciliation into errors.
	if syncer, ok := s.supervisor.(RuntimeTimingSyncer); ok {
		if wrote, err := syncer.SyncRuntimeTiming(machine.ID); err == nil && wrote {
			if latest, err := s.store.Get(machine.ID); err == nil {
				machine = latest
			}
		}
	}
	if s.supervisor.IsRunning(machine.ID) {
		return machine, nil
	}

	// Reread after checking the process because a concurrent termination may
	// have advanced the record while IsRunning waited for the supervisor lock.
	latest, err := s.store.Get(machine.ID)
	if err != nil {
		return Sandbox{}, err
	}
	if latest.State != StateRunning {
		return latest, nil
	}

	exit, _ := s.supervisor.RuntimeExit(machine.ID)
	failed, err := s.store.Fail(machine.ID, Failure{
		Reason:      FailureRuntimeExit,
		RuntimeExit: exit,
	}, s.now().UTC())
	if errors.Is(err, ErrInvalidTransition) {
		return s.store.Get(machine.ID)
	}
	if err != nil {
		return Sandbox{}, wrapStateStoreError("mark exited microVM failed", err)
	}
	return failed, nil
}

func classifyRuntimeStartError(err error) error {
	if errors.Is(err, ErrUnknownProfile) || errors.Is(err, ErrUnknownImage) || errors.Is(err, ErrUnsupportedShape) {
		return err
	}
	return errors.Join(ErrRuntimeUnavailable, err)
}

func wrapStateStoreError(operation string, err error) error {
	return errors.Join(ErrStateStore, fmt.Errorf("%s: %w", operation, err))
}

func wrapCleanupError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("clean up runtime after state failure: %w", err)
}

func validateCreateSpec(spec CreateSpec) error {
	if !resourceIDPattern.MatchString(spec.AppID) {
		return fmt.Errorf("%w: app ID is required", ErrInvalidSpec)
	}
	if ValidateWorkspaceID(spec.WorkspaceID) != nil {
		return fmt.Errorf("%w: workspace ID is required", ErrInvalidSpec)
	}
	return nil
}
