package supervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
)

var (
	ErrPIDFDUnsupported            = errors.New("Linux pidfds are unavailable")
	ErrInvalidPIDFDHandle          = errors.New("invalid process pidfd handle")
	ErrInvalidProcessIdentityState = errors.New("invalid process identity status")
	ErrInvalidTerminationTimeout   = errors.New("invalid process termination timeout")
)

type pidfdHandle interface {
	Signal(syscall.Signal) error
	Wait(context.Context) error
	Close() error
}

type openPIDFDFunc func(int) (pidfdHandle, error)
type verifyProcessIdentityFunc func(microvm.ProcessIdentity) (processIdentityStatus, error)

type processTerminationResult struct {
	Status processIdentityStatus
	Forced bool
}

// terminateVerifiedProcess keeps one verified pidfd open across graceful and
// forced termination, then confirms process exit before returning success.
func terminateVerifiedProcess(
	ctx context.Context,
	expected microvm.ProcessIdentity,
	graceTimeout time.Duration,
	forceTimeout time.Duration,
	openPIDFD openPIDFDFunc,
	verify verifyProcessIdentityFunc,
) (result processTerminationResult, err error) {
	if graceTimeout <= 0 || forceTimeout <= 0 {
		return processTerminationResult{}, fmt.Errorf(
			"%w: grace and force timeouts must be positive",
			ErrInvalidTerminationTimeout,
		)
	}

	handle, status, err := openVerifiedPIDFD(expected, openPIDFD, verify)
	result.Status = status
	if err != nil || handle == nil {
		return result, err
	}
	defer func() {
		err = errors.Join(err, closePIDFD(handle))
	}()

	if err := handle.Signal(syscall.SIGTERM); errors.Is(err, os.ErrProcessDone) {
		result.Status = processIdentityMissing
		return result, nil
	} else if err != nil {
		return result, fmt.Errorf("signal process through pidfd with SIGTERM: %w", err)
	}

	graceCtx, cancelGrace := context.WithTimeout(ctx, graceTimeout)
	waitErr := handle.Wait(graceCtx)
	graceCause := context.Cause(graceCtx)
	cancelGrace()
	if waitErr == nil {
		return result, nil
	}
	if graceCause == nil {
		return result, fmt.Errorf("wait for process exit after SIGTERM: %w", waitErr)
	}

	if err := handle.Signal(syscall.SIGKILL); errors.Is(err, os.ErrProcessDone) {
		result.Status = processIdentityMissing
		return result, nil
	} else if err != nil {
		return result, fmt.Errorf("signal process through pidfd with SIGKILL: %w", err)
	}
	result.Forced = true

	forceCtx, cancelForce := context.WithTimeout(context.WithoutCancel(ctx), forceTimeout)
	waitErr = handle.Wait(forceCtx)
	cancelForce()
	if waitErr != nil {
		return result, fmt.Errorf("wait for process exit after SIGKILL: %w", waitErr)
	}
	return result, nil
}

func openVerifiedPIDFD(
	expected microvm.ProcessIdentity,
	openPIDFD openPIDFDFunc,
	verify verifyProcessIdentityFunc,
) (pidfdHandle, processIdentityStatus, error) {
	if expected == (microvm.ProcessIdentity{}) {
		return nil, "", fmt.Errorf("open verified process: %w: identity is empty", microvm.ErrInvalidRuntimeOwnership)
	}
	if err := expected.Validate(); err != nil {
		return nil, "", fmt.Errorf("open verified process: %w", err)
	}

	handle, err := openPIDFD(expected.PID)
	if errors.Is(err, os.ErrNotExist) {
		return nil, processIdentityMissing, nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("open process pidfd: %w", err)
	}
	if handle == nil {
		return nil, "", ErrInvalidPIDFDHandle
	}

	status, err := verify(expected)
	if err != nil {
		return nil, "", errors.Join(err, closePIDFD(handle))
	}
	switch status {
	case processIdentityMissing, processIdentityDifferentBoot, processIdentityReusedPID:
		return nil, status, closePIDFD(handle)
	case processIdentityMatch:
		return handle, status, nil
	default:
		return nil, "", errors.Join(
			fmt.Errorf("%w: %q", ErrInvalidProcessIdentityState, status),
			closePIDFD(handle),
		)
	}
}

func closePIDFD(handle pidfdHandle) error {
	if err := handle.Close(); err != nil {
		return fmt.Errorf("close process pidfd: %w", err)
	}
	return nil
}
