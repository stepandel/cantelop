// Package microvm contains the domain model for managed Firecracker microVMs.
package microvm

import (
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/model"
)

// State describes the lifecycle phase currently observed by fire-fuse.
type State = model.State

const (
	StateActive     = model.StateActive
	StateIdle       = model.StateIdle
	StateStarting   = model.StateStarting
	StateRunning    = model.StateRunning
	StateStopping   = model.StateStopping
	StateTerminated = model.StateTerminated
	StateFailed     = model.StateFailed
)

// FailureReason is a stable, non-sensitive lifecycle failure category.
type FailureReason = model.FailureReason

const (
	FailureRuntimeStart = model.FailureRuntimeStart
	FailureRuntimeExit  = model.FailureRuntimeExit
)

// RuntimeExit describes how a supervised process ended.
type RuntimeExit struct {
	PID      int    `json:"pid"`
	ExitCode int    `json:"exit_code"`
	Signal   string `json:"signal"`
}

// Failure contains persisted diagnostics safe to expose through the API.
type Failure struct {
	Reason      FailureReason `json:"reason"`
	RuntimeExit RuntimeExit   `json:"runtime_exit"`
}

// NetworkAssignment contains the guest-visible identity of a microVM network
// interface. Host implementation details such as the TAP name remain owned by
// the runtime supervisor.
type NetworkAssignment = model.NetworkAssignment

// Sandbox is fire-fuse's stored view of one microVM. Runtime contains host-only
// recovery metadata and must not be exposed by API adapters.
type Sandbox struct {
	RuntimeTiming RuntimeTiming     `json:"runtime_timing"`
	ID            string            `json:"id"`
	AppID         string            `json:"app_id"`
	WorkspaceID   string            `json:"workspace_id"`
	Profile       string            `json:"profile"`
	BuildID       string            `json:"build_id"`
	VCPUs         int               `json:"vcpus"`
	MemoryMiB     int               `json:"memory_mib"`
	State         State             `json:"state"`
	Network       NetworkAssignment `json:"network"`
	Runtime       RuntimeOwnership  `json:"runtime"`
	IdleDeadline  time.Time         `json:"idle_deadline,omitempty"`
	Failure       Failure           `json:"failure"`
	// StartupCompletedAt and StartupDuration are recorded atomically when the
	// Sandbox first enters running. They remain stable across later lifecycle
	// transitions so callers can report one durable startup measurement.
	StartupCompletedAt time.Time     `json:"startup_completed_at"`
	StartupDuration    time.Duration `json:"startup_duration_ns"`
	CreatedAt          time.Time     `json:"created_at"`
	UpdatedAt          time.Time     `json:"updated_at"`
}

// CanTransition reports whether moving directly between two states is valid.
func CanTransition(from, to State) bool {
	return model.CanTransition(from, to)
}
