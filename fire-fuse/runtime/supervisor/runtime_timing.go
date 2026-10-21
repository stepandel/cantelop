package supervisor

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
)

// timedProcess captures exit independently of the supervisor's operation lock
// and resource cleanup. Writes are serialized and retried by ownership
// checkpoints and runtime reads, always using the original observation.
type timedProcess struct {
	exitOnce sync.Once
	managedProcess
	done    chan struct{}
	mu      sync.Mutex
	writeMu sync.Mutex
	timing  microvm.RuntimeTiming
	// persisted is the last timing the recorder accepted; guarded by writeMu.
	persisted microvm.RuntimeTiming
	record    microvm.RuntimeTimingRecorder
}

func observeProcess(process managedProcess, record microvm.RuntimeTimingRecorder) *timedProcess {
	p := &timedProcess{managedProcess: process, done: make(chan struct{}), record: record,
		timing: microvm.RuntimeTiming{Status: microvm.RuntimeTimingRunning, StartedAt: process.StartedAt()}}
	if p.timing.StartedAt.IsZero() {
		p.timing.StartedAt = time.Now().UTC()
	}
	go func() {
		<-process.Done()
		p.observeExit()
		_, _ = p.persist() // a read/cleanup retries failures without moving the timestamp
	}()
	return p
}

func (p *timedProcess) observeExit() {
	p.exitOnce.Do(func() {
		p.mu.Lock()
		p.timing.Status = microvm.RuntimeTimingStopped
		p.timing.StoppedAt = time.Now().UTC()
		if at := p.managedProcess.StoppedAt(); !at.IsZero() {
			p.timing.StoppedAt = at
		}
		if p.timing.StoppedAt.Before(p.timing.StartedAt) {
			p.timing.StoppedAt = p.timing.StartedAt
		}
		p.mu.Unlock()
		close(p.done)
	})
}
func (p *timedProcess) Done() <-chan struct{} {
	select {
	case <-p.managedProcess.Done():
		p.observeExit()
	default:
	}
	return p.done
}
func (p *timedProcess) Stop(ctx context.Context) error {
	err := p.managedProcess.Stop(ctx)
	select {
	case <-p.managedProcess.Done():
		p.observeExit()
	default:
	}
	return err
}
func (p *timedProcess) Kill(ctx context.Context) error {
	process, ok := p.managedProcess.(crashableProcess)
	if !ok {
		return microvm.ErrSupervisorUnavailable
	}
	return process.Kill(ctx)
}

// persist writes the current timing unless the recorder already accepted it.
// It reports whether a write happened.
func (p *timedProcess) persist() (bool, error) {
	_ = p.Done()
	if p.record == nil {
		return false, nil
	}
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	p.mu.Lock()
	timing := p.timing
	p.mu.Unlock()
	if timing == p.persisted {
		return false, nil
	}
	if err := p.record(timing); err != nil {
		return false, errors.Join(microvm.ErrStateStore, err)
	}
	p.persisted = timing
	return true, nil
}

// SyncRuntimeTiming retries an asynchronous exit write before returning a
// runtime observation. It never creates an interval for untracked/old VMs and
// never waits for a lifecycle operation; persist serializes it with the
// operation's own ownership checkpoints.
func (s *Supervisor) SyncRuntimeTiming(id string) (bool, error) {
	tracked, exists := s.trackedProcess(id)
	if !exists {
		return false, nil
	}
	if process, ok := tracked.(*timedProcess); ok {
		return process.persist()
	}
	return false, nil
}
