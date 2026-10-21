package microvm

import (
	"context"
	"fmt"
	"time"
)

// RuntimeTiming is an immutable runtime interval, independent of cleanup.
// Empty status identifies legacy records; unknown identifies interrupted
// measurements. Neither is evidence of zero usage. Pending means the current
// supervisor has not assigned/started a process yet.
type RuntimeTiming struct {
	Status    RuntimeTimingStatus `json:"status"`
	StartedAt time.Time           `json:"started_at,omitzero"`
	StoppedAt time.Time           `json:"stopped_at,omitzero"`
}

// RuntimeTimingStatus is the measurement state of a RuntimeTiming. The empty
// value is reserved for records written before timing existed.
type RuntimeTimingStatus string

const (
	RuntimeTimingPending    RuntimeTimingStatus = "pending"
	RuntimeTimingRunning    RuntimeTimingStatus = "running"
	RuntimeTimingStopped    RuntimeTimingStatus = "stopped"
	RuntimeTimingNotStarted RuntimeTimingStatus = "not_started"
	RuntimeTimingUnknown    RuntimeTimingStatus = "unknown"
)

func (t RuntimeTiming) Validate() error {
	valid := false
	switch t.Status {
	case "", RuntimeTimingPending, RuntimeTimingNotStarted:
		valid = t.StartedAt.IsZero() && t.StoppedAt.IsZero()
	case RuntimeTimingRunning:
		valid = !t.StartedAt.IsZero() && t.StoppedAt.IsZero()
	case RuntimeTimingStopped:
		valid = !t.StartedAt.IsZero() && !t.StoppedAt.Before(t.StartedAt)
	case RuntimeTimingUnknown:
		valid = t.StoppedAt.IsZero()
	}
	if !valid {
		return fmt.Errorf("%w: invalid runtime timing", ErrInvalidSandboxRecord)
	}
	return nil
}

func mergeRuntimeTiming(previous, next RuntimeTiming) (RuntimeTiming, error) {
	if err := next.Validate(); err != nil {
		return previous, err
	}
	if previous == next {
		return previous, nil
	}
	// A stopped interval survives cleanup, retries and restart reconciliation.
	if previous.Status == RuntimeTimingStopped || previous.Status == RuntimeTimingNotStarted {
		return previous, nil
	}
	if previous.Status == RuntimeTimingUnknown || previous.Status == "" {
		return previous, nil // never manufacture evidence for legacy/interrupted records
	}
	if !previous.StartedAt.IsZero() && !previous.StartedAt.Equal(next.StartedAt) {
		return previous, fmt.Errorf("%w: runtime start is immutable", ErrInvalidTransition)
	}
	if next.Status != RuntimeTimingRunning && next.Status != RuntimeTimingStopped && next.Status != RuntimeTimingUnknown && next.Status != RuntimeTimingNotStarted {
		return previous, fmt.Errorf("%w: invalid runtime timing transition", ErrInvalidTransition)
	}
	return next, nil
}

func (s *SQLiteStore) RecordRuntimeTiming(id string, timing RuntimeTiming) (Sandbox, error) {
	return s.mutate(id, func(machine Sandbox) (Sandbox, error) {
		next, err := mergeRuntimeTiming(machine.RuntimeTiming, timing)
		machine.RuntimeTiming = next
		return machine, err
	})
}

type runtimeTimingRecorderKey struct{}
type RuntimeTimingRecorder func(RuntimeTiming) error

// WithRuntimeTimingRecorder carries a durable accounting callback into the
// supervisor without coupling it to a particular persistence implementation.
// The callback must not depend on the request context remaining alive.
func WithRuntimeTimingRecorder(ctx context.Context, record RuntimeTimingRecorder) context.Context {
	return context.WithValue(ctx, runtimeTimingRecorderKey{}, record)
}

func RuntimeTimingRecorderFromContext(ctx context.Context) RuntimeTimingRecorder {
	record, _ := ctx.Value(runtimeTimingRecorderKey{}).(RuntimeTimingRecorder)
	return record
}
