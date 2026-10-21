package httpapi

import "sync"

// Only requests waiting on this Message are woken. Entries live for the wait,
// so completed Messages and idle Sessions retain no notification state.
type messageReplySignal struct {
	mu      sync.Mutex
	waiters map[string]map[chan struct{}]struct{}
}

func newMessageReplySignal() *messageReplySignal {
	return &messageReplySignal{waiters: make(map[string]map[chan struct{}]struct{})}
}
func (s *messageReplySignal) subscribe(id string) (<-chan struct{}, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := make(chan struct{})
	if s.waiters[id] == nil {
		s.waiters[id] = make(map[chan struct{}]struct{})
	}
	s.waiters[id][ch] = struct{}{}
	return ch, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.waiters[id], ch)
		if len(s.waiters[id]) == 0 {
			delete(s.waiters, id)
		}
	}
}
func (s *messageReplySignal) notify(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.waiters[id] {
		close(ch)
	}
	delete(s.waiters, id)
}
