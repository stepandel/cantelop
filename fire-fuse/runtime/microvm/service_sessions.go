package microvm

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/telemetry/warmuptrace"
)

const sessionActivationTimeout = 30 * time.Second

// CreateSessionAndStart creates a Session and starts its Sandbox.
func (s *Service) CreateSessionAndStart(
	ctx context.Context,
	sessionID string,
	workspaceID string,
	keepAliveSeconds int,
) (Session, error) {
	return s.createSessionAndStart(ctx, sessionID, workspaceID, keepAliveSeconds, "")
}

// OpenSessionAndStart resolves a durable Workspace-scoped key or atomically
// binds it to the proposed platform-generated Session ID.
func (s *Service) OpenSessionAndStart(
	ctx context.Context,
	sessionID string,
	workspaceID string,
	keepAliveSeconds int,
	keyHash string,
) (Session, error) {
	return s.createSessionAndStart(ctx, sessionID, workspaceID, keepAliveSeconds, keyHash)
}

func (s *Service) createSessionAndStart(
	ctx context.Context,
	sessionID string,
	workspaceID string,
	keepAliveSeconds int,
	keyHash string,
) (Session, error) {
	s.resourceMu.RLock()
	defer s.resourceMu.RUnlock()

	if !sessionIDPattern.MatchString(sessionID) {
		return Session{}, ErrInvalidSession
	}
	if ValidateWorkspaceID(workspaceID) != nil {
		return Session{}, ErrInvalidWorkspace
	}
	if keepAliveSeconds < MinSessionKeepAliveSeconds || keepAliveSeconds > MaxSessionKeepAliveSeconds {
		return Session{}, ErrInvalidSession
	}
	if keyHash != "" && !sessionKeyHashPattern.MatchString(keyHash) {
		return Session{}, ErrInvalidSession
	}
	if s.supervisor == nil {
		return Session{}, ErrSupervisorUnavailable
	}
	if keyHash != "" {
		existing, err := s.store.GetSessionByKey(workspaceID, keyHash)
		switch {
		case err == nil:
			return s.reopenSessionAndStart(ctx, existing, workspaceID, keepAliveSeconds, true)
		case !errors.Is(err, ErrSessionNotFound):
			return Session{}, wrapStateStoreError("look up keyed Session", err)
		}
	}

	existing, err := s.store.GetSession(sessionID)
	switch {
	case err == nil:
		return s.reopenSessionAndStart(ctx, existing, workspaceID, keepAliveSeconds, keyHash != "")
	case !errors.Is(err, ErrSessionNotFound):
		return Session{}, wrapStateStoreError("look up Session", err)
	}

	config, err := s.ResolveWorkspaceRuntime(workspaceID)
	if err != nil {
		return Session{}, err
	}
	profile, err := ResolveSandboxProfile(config.SessionRuntime.Runtime.Profile)
	if err != nil {
		return Session{}, errors.Join(ErrInvalidAppSessionRuntime, err)
	}
	now := s.now().UTC()
	sessionRecord := Session{
		ID: sessionID, WorkspaceID: workspaceID,
		BuildID: config.SessionRuntime.ActiveBuildID, KeepAliveSeconds: keepAliveSeconds,
		CreatedAt: now, UpdatedAt: now,
	}
	sandboxID, err := newResourceID("sbx-")
	if err != nil {
		return Session{}, err
	}
	machine := Sandbox{
		ID: sandboxID, AppID: config.Workspace.AppID,
		WorkspaceID: config.Workspace.ID,
		Profile:     profile.Name, BuildID: config.SessionRuntime.ActiveBuildID,
		VCPUs: profile.VCPUs, MemoryMiB: profile.MemoryMiB,
		RuntimeTiming: RuntimeTiming{Status: RuntimeTimingPending},
		State:         StateStarting, CreatedAt: now, UpdatedAt: now,
		IdleDeadline: now.Add(sessionActivationTimeout),
	}
	warmuptrace.SetMachineID(ctx, machine.ID)
	assignment := RuntimeSpec{
		WorkspaceID: machine.WorkspaceID, Profile: machine.Profile, BuildID: machine.BuildID,
		VCPUs: machine.VCPUs, MemoryMiB: machine.MemoryMiB,
	}
	if err := s.supervisor.Validate(assignment); err != nil {
		return Session{}, classifyRuntimeStartError(err)
	}

	stored, err := s.createSession(ctx, sessionRecord, machine, keyHash)
	if err != nil {
		return Session{}, err
	}
	resumed, err := s.resumeSessionCreate(ctx, stored)
	if err != nil {
		return resumed, err
	}
	if keyHash != "" && resumed.KeepAliveSeconds != keepAliveSeconds {
		resumed, err = s.applyAdmittedKeepAlive(resumed, workspaceID, keepAliveSeconds)
		if err != nil {
			return Session{}, err
		}
	}
	s.scheduleSessionDeadlines(resumed.ID, machine)
	return resumed, nil
}

func (s *Service) reopenSessionAndStart(
	ctx context.Context,
	existing Session,
	workspaceID string,
	keepAliveSeconds int,
	mutableKeepAlive bool,
) (Session, error) {
	if existing.WorkspaceID != workspaceID ||
		(!mutableKeepAlive && existing.KeepAliveSeconds != keepAliveSeconds) {
		return Session{}, ErrSessionConflict
	}
	unlock := s.sessionLocks.lock(existing.ID)
	defer unlock()
	machine, err := s.store.GetSandboxForSession(existing.ID)
	if err != nil {
		return Session{}, err
	}
	warmuptrace.SetMachineID(ctx, machine.ID)
	resumed, err := s.resumeSessionCreate(ctx, existing)
	if err != nil {
		return resumed, err
	}
	// Persist the requested idle policy only once the reopen is admitted.
	if mutableKeepAlive {
		resumed, err = s.applyAdmittedKeepAlive(resumed, workspaceID, keepAliveSeconds)
		if err != nil {
			return Session{}, err
		}
	}
	machine, err = s.store.GetSandboxForSession(existing.ID)
	if err != nil {
		return Session{}, err
	}
	if mutableKeepAlive && machine.State == StateRunning {
		leases, leaseErr := s.store.ListSessionWorkLeases(existing.ID)
		if leaseErr != nil {
			return Session{}, wrapStateStoreError("list Session work leases", leaseErr)
		}
		if len(leases) == 0 {
			now := s.now().UTC()
			machine, err = s.store.SetIdleDeadline(machine.ID, now.Add(sessionActivationTimeout), now)
			if err != nil {
				return Session{}, wrapStateStoreError("protect reopened Session activation", err)
			}
		}
	}
	s.scheduleSessionDeadlines(resumed.ID, machine)
	return resumed, nil
}

func (s *Service) applyAdmittedKeepAlive(
	resumed Session,
	workspaceID string,
	keepAliveSeconds int,
) (Session, error) {
	updated, err := s.store.UpdateSessionKeepAlive(resumed.ID, workspaceID, keepAliveSeconds, s.now().UTC())
	if err != nil {
		return Session{}, wrapStateStoreError("update keyed Session keepalive", err)
	}
	resumed.KeepAliveSeconds = updated.KeepAliveSeconds
	resumed.UpdatedAt = updated.UpdatedAt
	return resumed, nil
}

func (s *Service) scheduleSessionDeadlines(sessionID string, machine Sandbox) {
	deadline := machine.IdleDeadline
	if deadline.IsZero() {
		s.cancelSessionDeadline(sessionID)
		return
	}
	s.scheduleRuntimeTermination(sessionID, deadline)
}

func (s *Service) scheduleRuntimeTermination(sessionID string, deadline time.Time) {
	delay := deadline.Sub(s.now().UTC())
	if delay < 0 {
		delay = 0
	}

	s.deadlineMu.Lock()
	if existing := s.deadlineTimers[sessionID]; existing != nil {
		existing.Stop()
	}
	var timer *time.Timer
	timer = time.AfterFunc(delay, func() {
		unlock := s.sessionLocks.lock(sessionID)
		defer unlock()
		s.deadlineMu.Lock()
		if s.deadlineTimers[sessionID] != timer {
			s.deadlineMu.Unlock()
			return
		}
		delete(s.deadlineTimers, sessionID)
		s.deadlineMu.Unlock()

		if err := s.releaseSessionSandboxLocked(context.Background(), sessionID); err != nil {
			s.scheduleRuntimeTermination(sessionID, s.now().UTC().Add(time.Second))
		}
	})
	s.deadlineTimers[sessionID] = timer
	s.deadlineMu.Unlock()
}

func (s *Service) releaseSessionSandboxLocked(ctx context.Context, sessionID string) error {
	if _, err := s.store.GetSession(sessionID); err != nil {
		return err
	}
	machine, err := s.store.GetSandboxForSession(sessionID)
	if err != nil {
		return err
	}
	if machine.State == StateTerminated {
		return nil
	}
	_, err = s.Terminate(ctx, machine.ID)
	return err
}

func (s *Service) cancelSessionDeadline(sessionID string) {
	s.deadlineMu.Lock()
	if timer := s.deadlineTimers[sessionID]; timer != nil {
		timer.Stop()
		delete(s.deadlineTimers, sessionID)
	}
	s.deadlineMu.Unlock()
}

// RestoreSessionDeadlineSchedules restores TTL protection from durable
// Session records after startup recovery.
func (s *Service) RestoreSessionDeadlineSchedules() error {
	apps, err := s.store.ListApps()
	if err != nil {
		return wrapStateStoreError("restore Session deadline schedules", err)
	}
	for _, app := range apps {
		workspaces, listErr := s.store.ListWorkspaces(app.ID)
		if listErr != nil {
			return wrapStateStoreError("list Workspaces for deadline recovery", listErr)
		}
		for _, workspace := range workspaces {
			sessionRecords, instanceErr := s.store.ListSessions(workspace.ID)
			if instanceErr != nil {
				return wrapStateStoreError("list Sessions for deadline recovery", instanceErr)
			}
			for _, sessionRecord := range sessionRecords {
				machine, machineErr := s.store.GetSandboxForSession(sessionRecord.ID)
				if machineErr != nil {
					return machineErr
				}
				if machine.State == StateRunning {
					s.scheduleSessionDeadlines(sessionRecord.ID, machine)
				}
			}
		}
	}
	return nil
}

func (s *Service) createSession(
	ctx context.Context,
	sessionRecord Session,
	machine Sandbox,
	keyHash string,
) (Session, error) {
	lookup := func() (Session, bool, error) {
		existing, err := s.store.GetSession(sessionRecord.ID)
		if err == nil {
			if existing.WorkspaceID != sessionRecord.WorkspaceID ||
				(keyHash == "" && existing.KeepAliveSeconds != sessionRecord.KeepAliveSeconds) {
				return Session{}, true, ErrSessionConflict
			}
			return existing, true, nil
		}
		if !errors.Is(err, ErrSessionNotFound) {
			return Session{}, true, wrapStateStoreError("look up Session", err)
		}
		if keyHash != "" {
			existing, err = s.store.GetSessionByKey(sessionRecord.WorkspaceID, keyHash)
			if err == nil {
				return existing, true, nil
			}
			if !errors.Is(err, ErrSessionNotFound) {
				return Session{}, true, wrapStateStoreError("look up keyed Session", err)
			}
		}
		return Session{}, false, nil
	}
	// Replays return the existing Session without re-admission.
	if existing, found, err := lookup(); found {
		return existing, err
	}
	releaseManaged, managedErr := s.admitManaged(machine)
	if managedErr != nil {
		// A concurrent identical create commits before releasing the managed
		// lock this one waited on; return it rather than rejecting the replay.
		if existing, found, err := lookup(); found {
			return existing, err
		}
		return Session{}, managedErr
	}
	defer releaseManaged()

	if s.admission != nil {
		stored, _, err := s.admission.createSession(ctx, s.store, sessionRecord, machine, keyHash)
		return stored, err
	}
	stored, _, err := s.store.CreateSession(sessionRecord, machine, keyHash)
	if errors.Is(err, ErrSessionConflict) {
		return Session{}, err
	}
	if err != nil {
		return Session{}, wrapStateStoreError("record Session", err)
	}
	return stored, nil
}

func (s *Service) resumeSessionCreate(
	ctx context.Context,
	sessionRecord Session,
) (Session, error) {
	machine, err := s.store.GetSandboxForSession(sessionRecord.ID)
	if err != nil {
		return Session{}, err
	}
	switch machine.State {
	case StateStarting:
		if _, err := s.Start(ctx, machine.ID); err != nil {
			updated, getErr := s.store.GetSession(sessionRecord.ID)
			if getErr != nil {
				return Session{}, errors.Join(err, getErr)
			}
			return updated, err
		}
		return s.GetSession(sessionRecord.ID)
	case StateRunning:
		return s.GetSession(sessionRecord.ID)
	case StateStopping, StateTerminated, StateFailed:
		restarted, _, restartErr := s.reactivateSession(ctx, sessionRecord)
		return restarted, restartErr
	default:
		return Session{}, fmt.Errorf(
			"%w: cannot resume Session create from %s",
			ErrInvalidTransition,
			machine.State,
		)
	}
}

// GetSession returns the public lifecycle record after reconciling its
// private Sandbox with the supervisor.
func (s *Service) GetSession(sessionID string) (Session, error) {
	machine, err := s.store.GetSandboxForSession(sessionID)
	if err != nil {
		return Session{}, err
	}
	machine, err = s.reconcileRuntime(machine)
	if err != nil {
		return Session{}, err
	}
	sessionRecord, err := s.store.GetSession(sessionID)
	if err != nil {
		return Session{}, err
	}
	if !machine.StartupCompletedAt.IsZero() && machine.StartupDuration > 0 {
		sessionRecord.Startup = &SessionStartup{
			CompletedAt: machine.StartupCompletedAt,
			Duration:    machine.StartupDuration,
		}
	}
	sessionRecord.State = sessionStateForSandbox(machine.State)
	return sessionRecord, nil
}

func sessionStateForSandbox(state State) State {
	switch state {
	case StateStarting, StateRunning, StateStopping:
		return StateActive
	default:
		return StateIdle
	}
}

// ResolveSessionRoute returns the public Session and only the
// internal network data required to route traffic to it, reactivating an idle
// Session first. Only requests for the same Session wait on that activation.
func (s *Service) ResolveSessionRoute(ctx context.Context, sessionID string) (SessionRoute, error) {
	unlock := s.sessionLocks.lock(sessionID)
	defer unlock()
	sessionRecord, err := s.GetSession(sessionID)
	if err != nil {
		return SessionRoute{}, err
	}
	machine, err := s.store.GetSandboxForSession(sessionID)
	if err != nil {
		return SessionRoute{}, err
	}
	if machine.State != StateRunning {
		sessionRecord, machine, err = s.reactivateSession(ctx, sessionRecord)
		if err != nil {
			return SessionRoute{}, err
		}
	}
	return SessionRoute{Session: sessionRecord, SandboxID: machine.ID, Network: machine.Network}, nil
}

func (s *Service) reactivateSession(ctx context.Context, sessionRecord Session) (Session, Sandbox, error) {
	config, err := s.ResolveWorkspaceRuntime(sessionRecord.WorkspaceID)
	if err != nil {
		return Session{}, Sandbox{}, err
	}
	profile, err := ResolveSandboxProfile(config.SessionRuntime.Runtime.Profile)
	if err != nil {
		return Session{}, Sandbox{}, errors.Join(ErrInvalidAppSessionRuntime, err)
	}
	sandboxID, err := newResourceID("sbx-")
	if err != nil {
		return Session{}, Sandbox{}, err
	}
	now := s.now().UTC()
	machine := Sandbox{
		ID: sandboxID, AppID: config.Workspace.AppID, WorkspaceID: config.Workspace.ID,
		Profile: profile.Name, BuildID: config.SessionRuntime.ActiveBuildID,
		VCPUs: profile.VCPUs, MemoryMiB: profile.MemoryMiB,
		RuntimeTiming: RuntimeTiming{Status: RuntimeTimingPending},
		State:         StateStarting, CreatedAt: now, UpdatedAt: now,
		IdleDeadline: now.Add(sessionActivationTimeout),
	}
	if err := s.supervisor.Validate(RuntimeSpec{
		WorkspaceID: machine.WorkspaceID, Profile: machine.Profile, BuildID: machine.BuildID,
		VCPUs: machine.VCPUs, MemoryMiB: machine.MemoryMiB,
	}); err != nil {
		return Session{}, Sandbox{}, classifyRuntimeStartError(err)
	}
	sessionRecord, err = s.replaceSessionSandbox(ctx, sessionRecord.ID, machine, now)
	if err != nil {
		return Session{}, Sandbox{}, err
	}
	warmuptrace.SetMachineID(ctx, machine.ID)
	machine, err = s.Start(ctx, machine.ID)
	if err != nil {
		return sessionRecord, machine, err
	}
	s.scheduleSessionDeadlines(sessionRecord.ID, machine)
	sessionRecord.State = StateActive
	return sessionRecord, machine, nil
}

// replaceSessionSandbox admits and durably reserves a Starting replacement.
// Like Session creation, it holds the admission lock only until the Starting
// record exists: that record already counts against capacity, so the boot
// itself must not serialize every other admission on the host.
func (s *Service) replaceSessionSandbox(
	ctx context.Context,
	sessionID string,
	machine Sandbox,
	now time.Time,
) (Session, error) {
	// Managed admission precedes host admission; release both before native Start.
	releaseManaged, err := s.admitManaged(machine)
	if err != nil {
		return Session{}, err
	}
	defer releaseManaged()
	if s.admission != nil {
		s.admission.mu.Lock()
		defer s.admission.mu.Unlock()
		if err := s.admission.checkCapacity(ctx, s.store, machine); err != nil {
			return Session{}, err
		}
	}
	return s.store.ReplaceSessionSandbox(sessionID, machine, now)
}

// ListSessions returns the Sessions grouped by one Workspace.
func (s *Service) ListSessions(workspaceID string) ([]Session, error) {
	sessionRecords, err := s.store.ListSessions(workspaceID)
	if err != nil {
		return nil, err
	}
	for i := range sessionRecords {
		sessionRecords[i], err = s.GetSession(sessionRecords[i].ID)
		if err != nil {
			return nil, err
		}
	}
	return sessionRecords, nil
}

// TerminateSession releases the current private Sandbox. The public Session
// remains reusable and becomes active again when it is next resolved.
func (s *Service) TerminateSession(
	ctx context.Context,
	sessionID string,
) (Session, error) {
	unlock := s.sessionLocks.lock(sessionID)
	defer unlock()
	sessionRecord, err := s.store.GetSession(sessionID)
	if err != nil {
		return Session{}, err
	}
	machine, err := s.store.GetSandboxForSession(sessionID)
	if err != nil {
		return Session{}, err
	}
	// Fence transport ownership before stopping the runtime. A deliberate stop
	// must never trigger loss recovery or opt-in redelivery, including after a
	// coordinator restart or a failed runtime cleanup.
	if err := s.store.FenceSessionStop(sessionID, machine, s.now().UTC()); err != nil {
		return Session{}, wrapStateStoreError("fence Session stop", err)
	}
	if machine.State != StateTerminated {
		if _, err := s.Terminate(ctx, machine.ID); err != nil {
			updated, getErr := s.store.GetSession(sessionID)
			if getErr != nil {
				return Session{}, errors.Join(err, getErr)
			}
			return updated, err
		}
	}
	if err := s.revokeSessionWorkLeasesLocked(sessionID); err != nil {
		return Session{}, err
	}
	s.cancelSessionDeadline(sessionID)
	sessionRecord.State = StateIdle
	return sessionRecord, nil
}
