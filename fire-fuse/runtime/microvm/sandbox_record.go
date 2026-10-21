package microvm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

var (
	ErrInvalidSandboxRecord = errors.New("invalid persisted sandbox record")
	sandboxIDPattern        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,63}$`)
)

func encodeSandboxRecord(sandbox Sandbox) ([]byte, error) {
	if err := validatePersistedSandbox(sandbox); err != nil {
		return nil, err
	}
	record, err := json.Marshal(sandbox)
	if err != nil {
		return nil, fmt.Errorf("encode persisted sandbox record: %w", err)
	}
	return record, nil
}

func decodeSandboxRecord(record []byte) (Sandbox, error) {
	sandbox, err := decodeSandboxRecordDocument(record)
	if err != nil {
		return Sandbox{}, err
	}
	if err := validatePersistedSandbox(sandbox); err != nil {
		return Sandbox{}, err
	}
	return sandbox, nil
}

func decodeSandboxRecordDocument(record []byte) (Sandbox, error) {
	decoder := json.NewDecoder(bytes.NewReader(record))
	decoder.DisallowUnknownFields()

	var sandbox Sandbox
	if err := decoder.Decode(&sandbox); err != nil {
		return Sandbox{}, fmt.Errorf("%w: decode JSON: %v", ErrInvalidSandboxRecord, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Sandbox{}, fmt.Errorf("%w: record must contain exactly one JSON object", ErrInvalidSandboxRecord)
	}
	return sandbox, nil
}

func validatePersistedSandbox(machine Sandbox) error {
	if err := machine.RuntimeTiming.Validate(); err != nil {
		return err
	}
	invalid := func(detail string) error {
		return fmt.Errorf("%w: %s", ErrInvalidSandboxRecord, detail)
	}
	if err := ValidateSandboxID(machine.ID); err != nil {
		return errors.Join(ErrInvalidSandboxRecord, err)
	}
	if !resourceIDPattern.MatchString(machine.AppID) {
		return invalid("invalid app ID")
	}
	if err := ValidateWorkspaceID(machine.WorkspaceID); err != nil {
		return errors.Join(ErrInvalidSandboxRecord, err)
	}
	if strings.TrimSpace(machine.Profile) == "" || machine.VCPUs < 1 || machine.MemoryMiB < 1 {
		return invalid("invalid microVM specification")
	}
	if err := ValidateBuildID(machine.BuildID); err != nil {
		return errors.Join(ErrInvalidSandboxRecord, err)
	}
	switch machine.State {
	case StateStarting, StateRunning, StateStopping, StateTerminated, StateFailed:
	default:
		return invalid(fmt.Sprintf("invalid lifecycle state %q", machine.State))
	}
	if machine.CreatedAt.IsZero() || machine.UpdatedAt.IsZero() || machine.UpdatedAt.Before(machine.CreatedAt) {
		return invalid("invalid lifecycle timestamps")
	}
	if !machine.IdleDeadline.IsZero() && !machine.IdleDeadline.After(machine.CreatedAt) {
		return invalid("invalid idle deadline")
	}
	startupRecorded := !machine.StartupCompletedAt.IsZero() || machine.StartupDuration != 0
	if startupRecorded {
		if machine.StartupCompletedAt.IsZero() || machine.StartupDuration <= 0 {
			return invalid("incomplete startup measurement")
		}
		if machine.State == StateStarting ||
			!machine.StartupCompletedAt.Equal(machine.CreatedAt.Add(machine.StartupDuration)) ||
			machine.StartupCompletedAt.After(machine.UpdatedAt) {
			return invalid("invalid startup measurement")
		}
	}
	if machine.Network != (NetworkAssignment{}) {
		if err := validateOwnedNetwork(machine.Network); err != nil {
			return errors.Join(ErrInvalidSandboxRecord, err)
		}
	}
	if err := machine.Runtime.Validate(); err != nil {
		return errors.Join(ErrInvalidSandboxRecord, err)
	}
	if machine.State == StateStarting && machine.Network != (NetworkAssignment{}) {
		return invalid("starting microVM cannot expose a network assignment")
	}
	if machine.State == StateRunning && !hasCompleteRuntimeOwnership(machine) {
		return invalid("running microVM requires complete runtime ownership")
	}
	if machine.Network != (NetworkAssignment{}) &&
		machine.Runtime.Network != (NetworkAssignment{}) &&
		machine.Network != machine.Runtime.Network {
		return invalid("guest and runtime network assignments differ")
	}
	switch machine.Failure.Reason {
	case "", FailureRuntimeStart, FailureRuntimeExit:
	default:
		return invalid("invalid failure reason")
	}
	if machine.State == StateFailed && machine.Failure.Reason == "" {
		return invalid("failed microVM requires a failure reason")
	}
	if machine.Failure.Reason == "" && machine.Failure.RuntimeExit != (RuntimeExit{}) {
		return invalid("runtime exit requires a failure reason")
	}
	return nil
}

func ValidateSandboxID(id string) error {
	if !sandboxIDPattern.MatchString(id) {
		return errors.New("sandbox ID must be 1-64 ASCII letters, digits, or hyphens")
	}
	return nil
}

func hasCompleteRuntimeOwnership(machine Sandbox) bool {
	return machine.Network != (NetworkAssignment{}) &&
		machine.Runtime.JailPrepared &&
		machine.Runtime.AddressAllocated &&
		machine.Runtime.TAPCreated &&
		machine.Runtime.ForwardingApplied &&
		machine.Runtime.Process != (ProcessIdentity{})
}
