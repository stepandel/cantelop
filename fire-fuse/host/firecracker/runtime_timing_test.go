package firecracker

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"
)

func TestLauncherObservesProcessBeforeSocketTimeout(t *testing.T) {
	layout := testLauncherLayout(t)
	launcher := testLauncher(t, func(string, ...string) *exec.Cmd { return jailerHelperCommand("wait", layout.HostAPISocket) })
	launcher.config.SocketTimeout = 30 * time.Millisecond
	var observed *Process
	_, err := launcher.StartObserved(context.Background(), layout, func(p *Process) error {
		observed = p
		if p.StartedAt().IsZero() || !p.StoppedAt().IsZero() {
			t.Errorf("invalid live interval: %v - %v", p.StartedAt(), p.StoppedAt())
		}
		return nil
	})
	if !errors.Is(err, ErrAPISocketTimeout) || observed == nil {
		t.Fatalf("process=%v error=%v", observed, err)
	}
	if observed.StoppedAt().IsZero() || observed.StoppedAt().Before(observed.StartedAt()) {
		t.Fatal("early startup failure lost its process interval")
	}
	stopped := observed.StoppedAt()
	if err := observed.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if observed.StoppedAt() != stopped {
		t.Fatal("stop retry changed exit time")
	}
}

func TestLauncherAbortsWhenStartObservationCannotBePersisted(t *testing.T) {
	layout := testLauncherLayout(t)
	launcher := testLauncher(t, func(string, ...string) *exec.Cmd { return jailerHelperCommand("wait", layout.HostAPISocket) })
	failure := errors.New("timing store unavailable")
	var observed *Process
	_, err := launcher.StartObserved(context.Background(), layout, func(p *Process) error { observed = p; return failure })
	if !errors.Is(err, failure) || observed == nil {
		t.Fatalf("process=%v error=%v", observed, err)
	}
	select {
	case <-observed.Done():
	default:
		t.Fatal("unmetered process left running")
	}
	if observed.StoppedAt().IsZero() {
		t.Fatal("missing exit after persistence failure")
	}
}
