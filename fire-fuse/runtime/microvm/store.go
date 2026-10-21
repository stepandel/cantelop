package microvm

import (
	"errors"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/model"
)

var (
	ErrAlreadyExists     = model.ErrAlreadyExists
	ErrSandboxConflict   = errors.New("sandbox ID already used for a different request")
	ErrNotFound          = errors.New("sandbox not found")
	ErrInvalidTransition = errors.New("invalid sandbox state transition")
)

type SandboxReader interface {
	Get(id string) (Sandbox, error)
	List() ([]Sandbox, error)
	// ListActive omits terminated Sandboxes, which accumulate over time.
	ListActive() ([]Sandbox, error)
}

type SandboxCreator interface {
	Create(Sandbox) error
	CreateOrGet(Sandbox) (Sandbox, bool, error)
}

type SandboxRecoveryStore interface {
	RecordRuntimeTiming(id string, timing RuntimeTiming) (Sandbox, error)
	List() ([]Sandbox, error)
	CheckpointRuntime(id string, ownership RuntimeOwnership, at time.Time) (Sandbox, error)
	Transition(id string, next State, at time.Time) (Sandbox, error)
	Fail(id string, failure Failure, at time.Time) (Sandbox, error)
}

// SandboxStore owns only the durable lifecycle and recovery record for the
// internal Sandbox implementation.
type SandboxStore interface {
	SandboxReader
	SandboxCreator
	SandboxRecoveryStore
	Activate(id string, runtime RuntimeInfo, at time.Time) (Sandbox, error)
	SetIdleDeadline(id string, deadline, at time.Time) (Sandbox, error)
}

// Store composes the repositories required by the Runtime application service.
// Supporting workflows should accept narrower component ports instead.
type Store interface {
	SandboxStore
	ResourceStore
	ComputeProjectionStore
	WorkspaceStore
	ConfigStore
	SessionStore
	SessionWorkLeaseStore
	ReleaseAdmissionStore
}

func sameSandboxCreateRequest(left, right Sandbox) bool {
	return left.ID == right.ID && left.AppID == right.AppID &&
		left.Profile == right.Profile &&
		left.VCPUs == right.VCPUs && left.MemoryMiB == right.MemoryMiB
}

func maxTime(values ...time.Time) time.Time {
	latest := values[0]
	for _, value := range values[1:] {
		if value.After(latest) {
			latest = value
		}
	}
	return latest
}
