// Package warmuptrace records request-scoped microVM startup milestones.
package warmuptrace

import (
	"context"
	"sync"
	"time"
)

type contextKey struct{}

// Step is one measured warmup operation relative to the start of the request.
type Step struct {
	Name            string `json:"name"`
	StartedOffsetNS int64  `json:"started_offset_ns"`
	DurationNS      int64  `json:"duration_ns"`
}

// Snapshot is an immutable view of one warmup trace.
type Snapshot struct {
	MachineID string `json:"machine_id,omitempty"`
	TotalNS   int64  `json:"total_ns"`
	Steps     []Step `json:"steps"`
}

// Trace collects warmup operations that may complete concurrently.
type Trace struct {
	started time.Time

	mu        sync.Mutex
	machineID string
	steps     []Step
}

// New starts an empty warmup trace.
func New() *Trace {
	return &Trace{started: time.Now()}
}

// WithContext attaches trace to ctx.
func WithContext(ctx context.Context, trace *Trace) context.Context {
	if trace == nil {
		return ctx
	}
	return context.WithValue(ctx, contextKey{}, trace)
}

// FromContext returns the request warmup trace, if one is active.
func FromContext(ctx context.Context) *Trace {
	trace, _ := ctx.Value(contextKey{}).(*Trace)
	return trace
}

// Start begins a named operation and returns an idempotent completion function.
func Start(ctx context.Context, name string) func() {
	trace := FromContext(ctx)
	if trace == nil {
		return func() {}
	}
	started := time.Now()
	var once sync.Once
	return func() {
		once.Do(func() {
			finished := time.Now()
			trace.mu.Lock()
			defer trace.mu.Unlock()
			trace.steps = append(trace.steps, Step{
				Name:            name,
				StartedOffsetNS: started.Sub(trace.started).Nanoseconds(),
				DurationNS:      max(finished.Sub(started).Nanoseconds(), 1),
			})
		})
	}
}

// SetMachineID correlates the trace with the created microVM.
func SetMachineID(ctx context.Context, machineID string) {
	trace := FromContext(ctx)
	if trace == nil {
		return
	}
	trace.mu.Lock()
	defer trace.mu.Unlock()
	trace.machineID = machineID
}

// Snapshot returns a stable copy of the trace at the current instant.
func (t *Trace) Snapshot() Snapshot {
	if t == nil {
		return Snapshot{}
	}
	total := time.Since(t.started).Nanoseconds()
	t.mu.Lock()
	defer t.mu.Unlock()
	steps := append([]Step(nil), t.steps...)
	return Snapshot{
		MachineID: t.machineID,
		TotalNS:   max(total, 1),
		Steps:     steps,
	}
}
