package dispatcher

import "sync"

// A slow Sandbox must not hold up the next pass for every other Sandbox.
// Ownership stays serialized per Sandbox, with the existing global limit.
type reconciliationWorkers struct {
	mu          sync.Mutex
	running     map[string]bool
	slots       chan struct{}
	wg          sync.WaitGroup
	onAvailable func()
}

func newReconciliationWorkers() *reconciliationWorkers {
	return &reconciliationWorkers{running: make(map[string]bool), slots: make(chan struct{}, 16)}
}

func (w *reconciliationWorkers) start(sandboxID string, run func()) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.running[sandboxID] {
		return false
	}
	select {
	case w.slots <- struct{}{}:
	default:
		return false
	}
	w.running[sandboxID] = true
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		defer func() {
			w.mu.Lock()
			delete(w.running, sandboxID)
			<-w.slots
			w.mu.Unlock()
			if w.onAvailable != nil {
				w.onAvailable()
			}
		}()
		run()
	}()
	return true
}

func (w *reconciliationWorkers) isRunning(id string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.running[id]
}
