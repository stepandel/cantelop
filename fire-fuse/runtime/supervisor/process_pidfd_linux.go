//go:build linux

package supervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
	"golang.org/x/sys/unix"
)

type linuxPIDFD int

const pidfdPollInterval = 100 * time.Millisecond

func (fd linuxPIDFD) Signal(signal syscall.Signal) error {
	if err := unix.PidfdSendSignal(int(fd), unix.Signal(signal), nil, 0); errors.Is(err, unix.ESRCH) {
		return os.ErrProcessDone
	} else {
		return err
	}
}

func (fd linuxPIDFD) Wait(ctx context.Context) error {
	pollFDs := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		timeout := pidfdPollInterval
		if deadline, ok := ctx.Deadline(); ok {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return context.DeadlineExceeded
			}
			if remaining < timeout {
				timeout = remaining
			}
		}
		timeoutMillis := max(1, int((timeout+time.Millisecond-1)/time.Millisecond))
		ready, err := unix.Poll(pollFDs, timeoutMillis)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return fmt.Errorf("poll process pidfd: %w", err)
		}
		if ready == 0 {
			continue
		}
		events := pollFDs[0].Revents
		if events&unix.POLLIN != 0 {
			return nil
		}
		return fmt.Errorf("poll process pidfd: unexpected events %#x", events)
	}
}

func (fd linuxPIDFD) Close() error {
	return unix.Close(int(fd))
}

func openLinuxPIDFD(pid int) (pidfdHandle, error) {
	fd, err := unix.PidfdOpen(pid, 0)
	if errors.Is(err, unix.ESRCH) {
		return nil, os.ErrNotExist
	}
	if err != nil {
		return nil, err
	}
	return linuxPIDFD(fd), nil
}

func terminateVerifiedLinuxProcess(
	ctx context.Context,
	expected microvm.ProcessIdentity,
	graceTimeout time.Duration,
	forceTimeout time.Duration,
) (processTerminationResult, error) {
	return terminateVerifiedProcess(
		ctx,
		expected,
		graceTimeout,
		forceTimeout,
		openLinuxPIDFD,
		verifyLinuxProcessIdentity,
	)
}
