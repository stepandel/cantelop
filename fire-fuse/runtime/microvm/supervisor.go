package microvm

import "context"

// Supervisor owns the host-side runtime resources for a microVM.
//
// Implementations must make Start and Stop idempotent for a given Sandbox.ID.
// This lets the lifecycle service safely retry after an ambiguous failure.
type Supervisor interface {
	Validate(RuntimeSpec) error
	Start(context.Context, Sandbox, RuntimeCheckpoint) (RuntimeInfo, error)
	Stop(context.Context, Sandbox, RuntimeCheckpoint) error
	IsRunning(id string) bool
	RuntimeDone(id string) (<-chan struct{}, bool)
	RuntimeExit(id string) (RuntimeExit, bool)
}

// WorkspaceDataPurger removes an archived Workspace from durable host storage.
// Implementations must be idempotent because control-plane operations retry
// after ambiguous transport failures.
type WorkspaceDataPurger interface {
	PurgeWorkspace(context.Context, string) error
}

// RuntimeCrasher is an operator-only failure injection boundary. It kills the
// runtime process without lifecycle cleanup so normal exit observation and
// Session recovery see the same state as an unexpected host process loss.
type RuntimeCrasher interface {
	Crash(context.Context, string) error
}

// RuntimeTimingSyncer retries a pending runtime timing write before a read
// returns. It reports whether the store was written and must not block on an
// in-flight lifecycle operation.
type RuntimeTimingSyncer interface {
	SyncRuntimeTiming(id string) (bool, error)
}

// RuntimeUsageReader exposes an instantaneous host reading without making
// resource sampling part of the durable sandbox lifecycle contract.
type RuntimeUsageReader interface {
	RuntimeUsage(id string) (RuntimeUsage, error)
}

// HostCapacityReader exposes the instantaneous state of the fixed warm-slot
// pool without making it part of the durable Sandbox lifecycle contract.
type HostCapacityReader interface {
	HostCapacity() HostCapacity
}

// ProfileCapacityReader permits admission before a sandbox record is created.
// A zero Target means that synchronous preparation is enabled for this profile.
type ProfileCapacityReader interface {
	ProfileCapacity(string) HostCapacity
}

// HostCapacity counts fixed host-pool units across all configured profiles.
// Target is the desired pool size; the other fields describe units currently
// ready, claimed, or being prepared while the pool converges toward it.
type HostCapacity struct {
	Target    int
	Ready     int
	Claimed   int
	Preparing int
}

// RuntimeUsage is a live cgroup reading. CPUUsageUS is cumulative since the
// runtime entered its cgroup; MemoryCurrentBytes is instantaneous host RAM.
// GuestMemoryUsedBytes is guest MemTotal minus MemAvailable, excluding reclaimable
// cache. A nil value means no recent guest observation is available.
type RuntimeUsage struct {
	SandboxID            string
	CPUUsageUS           uint64
	MemoryCurrentBytes   uint64
	GuestMemoryMiB       int
	GuestMemoryUsedBytes *uint64
}

// BuildValidator is implemented by supervisors that can inspect the trusted
// physical image catalog independently of a runtime profile and shape.
type BuildValidator interface {
	ValidateBuild(string) error
}

// RuntimeSpec is the fully resolved immutable input to sandbox startup.
type RuntimeSpec struct {
	WorkspaceID string
	Profile     string
	BuildID     string
	VCPUs       int
	MemoryMiB   int
}

// RuntimeCheckpoint persists the host resources currently owned for one
// microVM. Supervisors call it after each successful acquisition or release.
type RuntimeCheckpoint func(RuntimeOwnership) error

// RuntimeInfo contains values discovered while starting a microVM. Network is
// safe to expose; Ownership is host-only recovery metadata.
type RuntimeInfo struct {
	Network   NetworkAssignment
	Ownership RuntimeOwnership
}
