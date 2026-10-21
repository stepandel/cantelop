//go:build !linux

package supervisor

import (
	"context"
	"fmt"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
)

func terminateVerifiedLinuxProcess(
	context.Context,
	microvm.ProcessIdentity,
	time.Duration,
	time.Duration,
) (processTerminationResult, error) {
	return processTerminationResult{}, fmt.Errorf("terminate verified process: %w", ErrPIDFDUnsupported)
}
