package microvm

import "sync"

// sessionLocks serializes lifecycle, route, and work-lease decisions for one
// Session without blocking unrelated Sessions. A slot exists only while some
// caller holds or awaits it, so idle Sessions retain no lock state.
//
// Callers must never hold two Session locks at once.
type sessionLocks struct {
	mu    sync.Mutex
	slots map[string]*sessionLockSlot
}

type sessionLockSlot struct {
	mu   sync.Mutex
	refs int
}

// lock blocks until the caller exclusively owns sessionID and returns the
// matching unlock function.
func (l *sessionLocks) lock(sessionID string) func() {
	l.mu.Lock()
	if l.slots == nil {
		l.slots = make(map[string]*sessionLockSlot)
	}
	slot := l.slots[sessionID]
	if slot == nil {
		slot = &sessionLockSlot{}
		l.slots[sessionID] = slot
	}
	slot.refs++
	l.mu.Unlock()

	slot.mu.Lock()
	return func() {
		slot.mu.Unlock()
		l.mu.Lock()
		slot.refs--
		if slot.refs == 0 {
			delete(l.slots, sessionID)
		}
		l.mu.Unlock()
	}
}
