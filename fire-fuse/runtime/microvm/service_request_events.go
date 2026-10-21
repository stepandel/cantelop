package microvm

// RecordRequestEvent validates and durably records one reverse-proxy request.
func (s *Service) RecordRequestEvent(event RequestEvent) (RequestEvent, bool, error) {
	store, ok := s.store.(RequestEventStore)
	if !ok {
		return RequestEvent{}, false, ErrRequestEventStore
	}
	return store.RecordRequestEvent(event)
}

// ListRequestEvents returns a bounded newest-first page from the durable event
// log. CPU and memory samples use a separate ephemeral path and never enter it.
func (s *Service) ListRequestEvents(query RequestEventQuery) (RequestEventPage, error) {
	store, ok := s.store.(RequestEventStore)
	if !ok {
		return RequestEventPage{}, ErrRequestEventStore
	}
	return store.ListRequestEvents(query)
}
