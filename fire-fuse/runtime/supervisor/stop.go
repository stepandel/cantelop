package supervisor

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
)

// Stop terminates a tracked process and releases its network and jail resources.
func (s *Supervisor) Stop(
	ctx context.Context,
	machine microvm.Sandbox,
	checkpoint microvm.RuntimeCheckpoint,
) error {
	slot, unlock := s.lockSlot(machine.ID)
	defer unlock()

	if slot.instance == nil {
		return nil
	}
	var stopErr error
	var guestShutdownErr error
	var workspaceShutdownStarted time.Time
	var forcedStopAttempted bool
	if slot.instance.process != nil && !processExited(slot.instance.process) {
		if slot.instance.workspaceEnabled {
			workspaceShutdownStarted = time.Now()
		}
		guestShutdownErr = s.requestGuestShutdown(ctx, slot.instance)
		if !processExited(slot.instance.process) {
			forcedStopAttempted = true
			stopErr = slot.instance.process.Stop(ctx)
		}
		if slot.instance.workspaceEnabled {
			outcome := "graceful"
			var observationErr error
			if guestShutdownErr != nil {
				outcome = "failed"
				observationErr = errors.Join(guestShutdownErr, stopErr)
			}
			if forcedStopAttempted && processExited(slot.instance.process) {
				outcome = "forced"
			} else if !processExited(slot.instance.process) {
				outcome = "failed"
			}
			s.deps.recordWorkspace(workspaceLifecycleObservation{
				MachineID: machine.ID,
				Event:     "shutdown",
				Outcome:   outcome,
				Duration:  observedDuration(workspaceShutdownStarted),
				Err:       observationErr,
			})
		}
		if !processExited(slot.instance.process) {
			return errors.Join(wrapGuestShutdownError(guestShutdownErr), stopErr)
		}
	}
	if slot.instance.processIdentity != (microvm.ProcessIdentity{}) {
		identity := slot.instance.processIdentity
		slot.instance.processIdentity = microvm.ProcessIdentity{}
		if err := checkpointOwnership(checkpoint, slot.instance, "stopping the Firecracker process"); err != nil {
			slot.instance.processIdentity = identity
			return errors.Join(
				wrapGuestShutdownError(guestShutdownErr),
				wrapStopError(stopErr),
				err,
			)
		}
	}
	cleanupErr := s.cleanupInstance(ctx, machine.ID, slot.instance, checkpoint)
	if cleanupErr == nil {
		slot.setInstance(nil)
	}
	result := errors.Join(
		wrapStopError(stopErr),
		cleanupErr,
	)
	if result != nil {
		return errors.Join(wrapGuestShutdownError(guestShutdownErr), result)
	}
	return nil
}

func (s *Supervisor) requestGuestShutdown(ctx context.Context, owned *instance) error {
	client, err := s.deps.newControlClient(owned.layout.HostAPISocket)
	if err != nil {
		return fmt.Errorf("create Firecracker API client: %w", err)
	}
	defer client.CloseIdleConnections()

	shutdownCtx, cancel := context.WithTimeout(
		ctx,
		time.Duration(s.config.Timeouts.GuestShutdown),
	)
	defer cancel()
	if err := client.SendCtrlAltDel(shutdownCtx); err != nil {
		return fmt.Errorf("send Ctrl+Alt+Del to guest: %w", err)
	}
	select {
	case <-owned.process.Done():
		return nil
	case <-shutdownCtx.Done():
		return fmt.Errorf("wait for graceful guest shutdown: %w", context.Cause(shutdownCtx))
	}
}

// Crash kills a tracked Firecracker process without changing durable lifecycle
// state or releasing resources. The lifecycle watcher records the runtime exit
// and the ordinary failed-Sandbox cleanup/recovery path owns what happens next.
func (s *Supervisor) Crash(ctx context.Context, id string) error {
	slot := s.lookupSlot(id)
	if slot == nil {
		return microvm.ErrRuntimeUnavailable
	}

	slot.mu.Lock()
	defer slot.mu.Unlock()
	if slot.instance == nil || slot.instance.process == nil || processExited(slot.instance.process) {
		return microvm.ErrInvalidTransition
	}
	process, ok := slot.instance.process.(crashableProcess)
	if !ok {
		return microvm.ErrSupervisorUnavailable
	}
	if err := process.Kill(ctx); err != nil {
		return fmt.Errorf("crash Firecracker process: %w", err)
	}
	return nil
}

// RuntimeExit returns sanitized exit information for a completed process.
func (s *Supervisor) RuntimeExit(id string) (microvm.RuntimeExit, bool) {
	process, exists := s.trackedProcess(id)
	if !exists || !processExited(process) {
		return microvm.RuntimeExit{}, false
	}
	return newRuntimeExit(process), true
}

func newRuntimeExit(process managedProcess) microvm.RuntimeExit {
	exit := microvm.RuntimeExit{PID: process.PID()}
	err := process.Wait()
	if err == nil {
		return exit
	}

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		exit.ExitCode = -1
		return exit
	}
	if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		exit.Signal = status.Signal().String()
		return exit
	}
	exit.ExitCode = exitErr.ExitCode()
	return exit
}
