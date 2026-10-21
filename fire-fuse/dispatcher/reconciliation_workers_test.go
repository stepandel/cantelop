package dispatcher

import (
	"fmt"
	"testing"
	"time"
)

func TestSlowReconciliationDoesNotBlockNewSandboxOrOverlapSameSandbox(t *testing.T) {
	w := newReconciliationWorkers()
	blocked, started, progressed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer func() { close(blocked); w.wg.Wait() }()
	if !w.start("old", func() { close(started); <-blocked }) {
		t.Fatal("old Sandbox not started")
	}
	<-started
	if w.start("old", func() { t.Error("same Sandbox overlapped") }) {
		t.Fatal("overlap admitted")
	}
	if !w.start("new", func() { close(progressed) }) {
		t.Fatal("new Sandbox not started")
	}
	select {
	case <-progressed:
	case <-time.After(time.Second):
		t.Fatal("new Sandbox waited for old reconciliation")
	}
}

func TestReconciliationRemainsBoundedAndReleasesCapacity(t *testing.T) {
	w := newReconciliationWorkers()
	blocked := make(chan struct{})
	for i := 0; i < 16; i++ {
		if !w.start(fmt.Sprint(i), func() { <-blocked }) {
			t.Fatal("capacity exhausted early")
		}
	}
	if w.start("extra", func() {}) {
		t.Fatal("concurrency limit exceeded")
	}
	close(blocked)
	w.wg.Wait()
	if !w.start("0", func() {}) {
		t.Fatal("finished Sandbox remained blocked")
	}
	w.wg.Wait()
}
