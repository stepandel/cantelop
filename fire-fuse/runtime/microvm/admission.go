package microvm

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/stepandel/cantelop/fire-fuse/telemetry/warmuptrace"
)

var (
	ErrAdmissionLimit         = errors.New("microVM admission limit reached")
	ErrInvalidAdmissionLimits = errors.New("invalid microVM admission limits")
)

// AdmissionLimits defines host-wide resource and startup concurrency ceilings.
type AdmissionLimits struct {
	MaxVMs              int
	MaxTotalMemoryMiB   int
	MaxConcurrentStarts int
}

type admissionController struct {
	limits          AdmissionLimits
	mu              sync.Mutex
	starts          chan struct{}
	profileCapacity func(string) HostCapacity
}

type admissionStore interface {
	SandboxReader
	SandboxCreator
	WorkspaceStore
	SessionStore
}

func newAdmissionController(limits AdmissionLimits) (*admissionController, error) {
	if limits.MaxVMs < 1 || limits.MaxTotalMemoryMiB < 1 ||
		limits.MaxConcurrentStarts < 1 || limits.MaxConcurrentStarts > limits.MaxVMs {
		return nil, ErrInvalidAdmissionLimits
	}
	return &admissionController{
		limits: limits,
		starts: make(chan struct{}, limits.MaxConcurrentStarts),
	}, nil
}

func (a *admissionController) create(ctx context.Context, store admissionStore, machine Sandbox) error {
	finishLock := warmuptrace.Start(ctx, "admission.acquire_capacity_lock")
	a.mu.Lock()
	finishLock()
	defer a.mu.Unlock()

	if err := a.checkCapacity(ctx, store, machine); err != nil {
		return err
	}
	finishCreate := warmuptrace.Start(ctx, "store.create_machine")
	if err := store.Create(machine); err != nil {
		finishCreate()
		return wrapStateStoreError("record microVM", err)
	}
	finishCreate()
	return nil
}

func (a *admissionController) createOrGet(
	ctx context.Context,
	store admissionStore,
	machine Sandbox,
) (Sandbox, bool, error) {
	finishLock := warmuptrace.Start(ctx, "admission.acquire_capacity_lock")
	a.mu.Lock()
	finishLock()
	defer a.mu.Unlock()

	existing, err := store.Get(machine.ID)
	if err == nil {
		if !sameSandboxCreateRequest(existing, machine) {
			return Sandbox{}, false, ErrSandboxConflict
		}
		return existing, false, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Sandbox{}, false, wrapStateStoreError("look up sandbox admission", err)
	}
	if err := a.checkCapacity(ctx, store, machine); err != nil {
		return Sandbox{}, false, err
	}
	stored, created, err := store.CreateOrGet(machine)
	if errors.Is(err, ErrSandboxConflict) {
		return Sandbox{}, false, err
	}
	if err != nil {
		return Sandbox{}, false, wrapStateStoreError("record sandbox", err)
	}
	return stored, created, nil
}

func (a *admissionController) createSession(
	ctx context.Context,
	store admissionStore,
	instance Session,
	machine Sandbox,
	keyHash string,
) (Session, bool, error) {
	finishLock := warmuptrace.Start(ctx, "admission.acquire_capacity_lock")
	a.mu.Lock()
	finishLock()
	defer a.mu.Unlock()

	if err := a.checkCapacity(ctx, store, machine); err != nil {
		return Session{}, false, err
	}
	created, inserted, err := store.CreateSession(instance, machine, keyHash)
	if err != nil {
		return Session{}, false, wrapStateStoreError("record Session", err)
	}
	return created, inserted, nil
}

func (a *admissionController) checkCapacity(ctx context.Context, store SandboxReader, machine Sandbox) error {
	finishList := warmuptrace.Start(ctx, "store.list_for_admission")
	machines, err := store.ListActive()
	finishList()
	if err != nil {
		return wrapStateStoreError("calculate admission usage", err)
	}

	finishCapacity := warmuptrace.Start(ctx, "admission.calculate_capacity")
	var vmCount, profileCount, memoryMiB int
	var memoryExceeded bool
	for _, existing := range machines {
		if !reservesAdmission(existing) {
			continue
		}
		vmCount++
		if existing.Profile == machine.Profile {
			profileCount++
		}
		memoryMiB, memoryExceeded = addWithinLimit(memoryMiB, existing.MemoryMiB, a.limits.MaxTotalMemoryMiB, memoryExceeded)
	}
	if a.profileCapacity != nil {
		capacity := a.profileCapacity(machine.Profile)
		// Starting records reserve ready slots even before the supervisor claims
		// them. Count them under the admission lock so concurrent requests cannot
		// all pass a check of Ready alone. Preparing slots are not yet usable.
		if capacity.Target > 0 && (capacity.Ready == 0 || profileCount >= capacity.Ready+capacity.Claimed) {
			finishCapacity()
			return fmt.Errorf("%w: no prepared host capacity is available for profile %s", ErrAdmissionLimit, machine.Profile)
		}
	}

	if exceedsLimit(vmCount, 1, a.limits.MaxVMs) {
		finishCapacity()
		return fmt.Errorf("%w: maximum active VM count is %d", ErrAdmissionLimit, a.limits.MaxVMs)
	}
	if memoryExceeded || exceedsLimit(memoryMiB, machine.MemoryMiB, a.limits.MaxTotalMemoryMiB) {
		finishCapacity()
		return fmt.Errorf("%w: maximum total memory is %d MiB", ErrAdmissionLimit, a.limits.MaxTotalMemoryMiB)
	}
	finishCapacity()
	return nil
}

func (a *admissionController) acquireStart(ctx context.Context) (func(), error) {
	select {
	case a.starts <- struct{}{}:
		return func() { <-a.starts }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func reservesAdmission(machine Sandbox) bool {
	switch machine.State {
	case StateStarting, StateRunning, StateStopping:
		return true
	case StateFailed:
		return machine.Runtime != (RuntimeOwnership{})
	default:
		return false
	}
}

func exceedsLimit(current, additional, limit int) bool {
	return current > limit || additional > limit || current > limit-additional
}

func addWithinLimit(current, additional, limit int, alreadyExceeded bool) (int, bool) {
	if alreadyExceeded || additional < 0 || exceedsLimit(current, additional, limit) {
		return current, true
	}
	return current + additional, false
}
