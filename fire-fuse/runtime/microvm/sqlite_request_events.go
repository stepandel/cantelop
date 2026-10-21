package microvm

var _ RequestEventStore = (*SQLiteStore)(nil)

func (s *SQLiteStore) RecordRequestEvent(event RequestEvent) (RequestEvent, bool, error) {
	return s.requestEvents.RecordRequestEvent(event)
}

func (s *SQLiteStore) ListRequestEvents(query RequestEventQuery) (RequestEventPage, error) {
	return s.requestEvents.ListRequestEvents(query)
}
