package supervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"syscall"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
)

type fakePIDFD struct {
	operations *[]string
	signalErr  error
	closeErr   error
	wait       func(context.Context) error
}

func (f *fakePIDFD) Signal(signal syscall.Signal) error {
	*f.operations = append(*f.operations, "signal "+signal.String())
	return f.signalErr
}

func (f *fakePIDFD) Wait(ctx context.Context) error {
	*f.operations = append(*f.operations, "wait")
	if f.wait != nil {
		return f.wait(ctx)
	}
	return nil
}

func (f *fakePIDFD) Close() error {
	*f.operations = append(*f.operations, "close")
	return f.closeErr
}

func TestSignalVerifiedProcess(t *testing.T) {
	expected := testPersistedProcessIdentity()
	var operations []string
	handle := &fakePIDFD{operations: &operations}

	status, err := signalVerifiedProcess(
		expected,
		syscall.SIGTERM,
		func(pid int) (pidfdHandle, error) {
			operations = append(operations, "open")
			if pid != expected.PID {
				t.Errorf("opened PID = %d, want %d", pid, expected.PID)
			}
			return handle, nil
		},
		func(identity microvm.ProcessIdentity) (processIdentityStatus, error) {
			operations = append(operations, "verify")
			if identity != expected {
				t.Errorf("verified identity = %#v, want %#v", identity, expected)
			}
			return processIdentityMatch, nil
		},
	)
	if err != nil {
		t.Fatalf("signalVerifiedProcess() error = %v", err)
	}
	if status != processIdentityMatch {
		t.Errorf("signalVerifiedProcess() status = %q, want %q", status, processIdentityMatch)
	}
	wantOperations := []string{"open", "verify", "signal terminated", "close"}
	if !reflect.DeepEqual(operations, wantOperations) {
		t.Errorf("operations = %v, want %v", operations, wantOperations)
	}
}

func TestSignalVerifiedProcessDoesNotSignalStaleIdentity(t *testing.T) {
	expected := testPersistedProcessIdentity()
	for _, status := range []processIdentityStatus{
		processIdentityMissing,
		processIdentityDifferentBoot,
		processIdentityReusedPID,
	} {
		t.Run(string(status), func(t *testing.T) {
			var operations []string
			handle := &fakePIDFD{operations: &operations}
			got, err := signalVerifiedProcess(
				expected,
				syscall.SIGKILL,
				func(int) (pidfdHandle, error) {
					operations = append(operations, "open")
					return handle, nil
				},
				func(microvm.ProcessIdentity) (processIdentityStatus, error) {
					operations = append(operations, "verify")
					return status, nil
				},
			)
			if err != nil {
				t.Fatalf("signalVerifiedProcess() error = %v", err)
			}
			if got != status {
				t.Errorf("signalVerifiedProcess() status = %q, want %q", got, status)
			}
			if want := []string{"open", "verify", "close"}; !reflect.DeepEqual(operations, want) {
				t.Errorf("operations = %v, want %v", operations, want)
			}
		})
	}
}

func TestSignalVerifiedProcessHandlesProcessExitRaces(t *testing.T) {
	expected := testPersistedProcessIdentity()

	t.Run("before pidfd open", func(t *testing.T) {
		status, err := signalVerifiedProcess(
			expected,
			syscall.SIGTERM,
			func(int) (pidfdHandle, error) { return nil, os.ErrNotExist },
			func(microvm.ProcessIdentity) (processIdentityStatus, error) {
				t.Fatal("verify called without a pidfd")
				return "", nil
			},
		)
		if err != nil || status != processIdentityMissing {
			t.Fatalf("signalVerifiedProcess() = %q, %v; want missing, nil", status, err)
		}
	})

	t.Run("after identity verification", func(t *testing.T) {
		var operations []string
		handle := &fakePIDFD{operations: &operations, signalErr: os.ErrProcessDone}
		status, err := signalVerifiedProcess(
			expected,
			syscall.SIGTERM,
			func(int) (pidfdHandle, error) { return handle, nil },
			func(microvm.ProcessIdentity) (processIdentityStatus, error) {
				return processIdentityMatch, nil
			},
		)
		if err != nil || status != processIdentityMissing {
			t.Fatalf("signalVerifiedProcess() = %q, %v; want missing, nil", status, err)
		}
	})
}

func TestSignalVerifiedProcessReturnsOperationalErrors(t *testing.T) {
	expected := testPersistedProcessIdentity()
	openErr := errors.New("pidfd open failed")
	verifyErr := errors.New("proc read failed")
	signalErr := errors.New("signal failed")
	closeErr := errors.New("close failed")

	tests := []struct {
		name      string
		openErr   error
		verifyErr error
		signalErr error
		closeErr  error
		wantErr   error
	}{
		{name: "open", openErr: openErr, wantErr: openErr},
		{name: "nil handle", wantErr: ErrInvalidPIDFDHandle},
		{name: "verify", verifyErr: verifyErr, wantErr: verifyErr},
		{name: "invalid status", wantErr: ErrInvalidProcessIdentityState},
		{name: "signal", signalErr: signalErr, wantErr: signalErr},
		{name: "close", closeErr: closeErr, wantErr: closeErr},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handle := &fakePIDFD{operations: &[]string{}, signalErr: test.signalErr, closeErr: test.closeErr}
			_, err := signalVerifiedProcess(
				expected,
				syscall.SIGTERM,
				func(int) (pidfdHandle, error) {
					if test.name == "nil handle" {
						return nil, nil
					}
					return handle, test.openErr
				},
				func(microvm.ProcessIdentity) (processIdentityStatus, error) {
					if test.name == "invalid status" {
						return processIdentityStatus("unexpected"), nil
					}
					return processIdentityMatch, test.verifyErr
				},
			)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("signalVerifiedProcess() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestSignalVerifiedProcessRejectsEmptyIdentityBeforeOpeningPIDFD(t *testing.T) {
	opened := false
	_, err := signalVerifiedProcess(
		microvm.ProcessIdentity{},
		syscall.SIGTERM,
		func(int) (pidfdHandle, error) {
			opened = true
			return nil, nil
		},
		func(microvm.ProcessIdentity) (processIdentityStatus, error) { return "", nil },
	)
	if !errors.Is(err, microvm.ErrInvalidRuntimeOwnership) {
		t.Fatalf("signalVerifiedProcess() error = %v, want invalid ownership", err)
	}
	if opened {
		t.Fatal("pidfd opened for an invalid identity")
	}
}

func TestTerminateVerifiedProcessGracefulExit(t *testing.T) {
	expected := testPersistedProcessIdentity()
	var operations []string
	handle := &fakePIDFD{operations: &operations}

	result, err := terminateVerifiedProcess(
		context.Background(),
		expected,
		time.Second,
		time.Second,
		func(int) (pidfdHandle, error) {
			operations = append(operations, "open")
			return handle, nil
		},
		func(microvm.ProcessIdentity) (processIdentityStatus, error) {
			operations = append(operations, "verify")
			return processIdentityMatch, nil
		},
	)
	if err != nil {
		t.Fatalf("terminateVerifiedProcess() error = %v", err)
	}
	wantResult := processTerminationResult{Status: processIdentityMatch}
	if result != wantResult {
		t.Errorf("terminateVerifiedProcess() = %#v, want %#v", result, wantResult)
	}
	wantOperations := []string{"open", "verify", "signal terminated", "wait", "close"}
	if !reflect.DeepEqual(operations, wantOperations) {
		t.Errorf("operations = %v, want %v", operations, wantOperations)
	}
}

func TestTerminateVerifiedProcessForceKillsAfterGraceTimeout(t *testing.T) {
	expected := testPersistedProcessIdentity()
	var operations []string
	waits := 0
	handle := &fakePIDFD{
		operations: &operations,
		wait: func(ctx context.Context) error {
			waits++
			if waits == 1 {
				<-ctx.Done()
				return ctx.Err()
			}
			return nil
		},
	}

	result, err := terminateVerifiedProcess(
		context.Background(),
		expected,
		time.Millisecond,
		time.Second,
		func(int) (pidfdHandle, error) {
			operations = append(operations, "open")
			return handle, nil
		},
		func(microvm.ProcessIdentity) (processIdentityStatus, error) {
			operations = append(operations, "verify")
			return processIdentityMatch, nil
		},
	)
	if err != nil {
		t.Fatalf("terminateVerifiedProcess() error = %v", err)
	}
	wantResult := processTerminationResult{Status: processIdentityMatch, Forced: true}
	if result != wantResult {
		t.Errorf("terminateVerifiedProcess() = %#v, want %#v", result, wantResult)
	}
	wantOperations := []string{
		"open", "verify", "signal terminated", "wait", "signal killed", "wait", "close",
	}
	if !reflect.DeepEqual(operations, wantOperations) {
		t.Errorf("operations = %v, want %v", operations, wantOperations)
	}
}

func TestTerminateVerifiedProcessDoesNotSignalStaleIdentity(t *testing.T) {
	expected := testPersistedProcessIdentity()
	var operations []string
	handle := &fakePIDFD{operations: &operations}

	result, err := terminateVerifiedProcess(
		context.Background(),
		expected,
		time.Second,
		time.Second,
		func(int) (pidfdHandle, error) {
			operations = append(operations, "open")
			return handle, nil
		},
		func(microvm.ProcessIdentity) (processIdentityStatus, error) {
			operations = append(operations, "verify")
			return processIdentityReusedPID, nil
		},
	)
	if err != nil {
		t.Fatalf("terminateVerifiedProcess() error = %v", err)
	}
	if result.Status != processIdentityReusedPID || result.Forced {
		t.Errorf("terminateVerifiedProcess() = %#v, want reused PID without force", result)
	}
	if want := []string{"open", "verify", "close"}; !reflect.DeepEqual(operations, want) {
		t.Errorf("operations = %v, want %v", operations, want)
	}
}

func TestTerminateVerifiedProcessRequiresForcedExitConfirmation(t *testing.T) {
	expected := testPersistedProcessIdentity()
	var operations []string
	handle := &fakePIDFD{
		operations: &operations,
		wait: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}

	result, err := terminateVerifiedProcess(
		context.Background(),
		expected,
		time.Millisecond,
		time.Millisecond,
		func(int) (pidfdHandle, error) { return handle, nil },
		func(microvm.ProcessIdentity) (processIdentityStatus, error) {
			return processIdentityMatch, nil
		},
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("terminateVerifiedProcess() error = %v, want deadline exceeded", err)
	}
	if !result.Forced {
		t.Error("terminateVerifiedProcess() did not record SIGKILL escalation")
	}
	wantOperations := []string{"signal terminated", "wait", "signal killed", "wait", "close"}
	if !reflect.DeepEqual(operations, wantOperations) {
		t.Errorf("operations = %v, want %v", operations, wantOperations)
	}
}

func TestTerminateVerifiedProcessRejectsInvalidTimeouts(t *testing.T) {
	opened := false
	_, err := terminateVerifiedProcess(
		context.Background(),
		testPersistedProcessIdentity(),
		0,
		time.Second,
		func(int) (pidfdHandle, error) {
			opened = true
			return nil, nil
		},
		func(microvm.ProcessIdentity) (processIdentityStatus, error) { return processIdentityMatch, nil },
	)
	if !errors.Is(err, ErrInvalidTerminationTimeout) {
		t.Fatalf("terminateVerifiedProcess() error = %v, want invalid timeout", err)
	}
	if opened {
		t.Fatal("pidfd opened with invalid termination timeouts")
	}
}

func testPersistedProcessIdentity() microvm.ProcessIdentity {
	return microvm.ProcessIdentity{
		PID:            123,
		BootID:         "01234567-89ab-cdef-0123-456789abcdef",
		StartTimeTicks: 4242,
	}
}

// signalVerifiedProcess opens a stable process handle before checking the
// persisted identity. Once opened, the pidfd cannot retarget a reused PID.
func signalVerifiedProcess(
	expected microvm.ProcessIdentity,
	signal syscall.Signal,
	openPIDFD openPIDFDFunc,
	verify verifyProcessIdentityFunc,
) (status processIdentityStatus, err error) {
	handle, status, err := openVerifiedPIDFD(expected, openPIDFD, verify)
	if err != nil || handle == nil {
		return status, err
	}
	defer func() {
		err = errors.Join(err, closePIDFD(handle))
	}()

	if err := handle.Signal(signal); errors.Is(err, os.ErrProcessDone) {
		return processIdentityMissing, nil
	} else if err != nil {
		return "", fmt.Errorf("signal process through pidfd: %w", err)
	}
	return processIdentityMatch, nil
}
