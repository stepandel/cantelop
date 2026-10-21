package microvm

import (
	sqlitepersistence "github.com/stepandel/cantelop/fire-fuse/runtime/persistence/sqlite"
	"github.com/stepandel/cantelop/fire-fuse/sessionevents"
)

func (s *SQLiteStore) SessionEventStore() sessionevents.Store {
	// One shared instance keeps cached usage and reader wakeups consistent.
	s.sessionEventsOnce.Do(func() {
		s.sessionEvents = sqlitepersistence.NewSessionEvents(s.database.SQL(), s.configCipher)
	})
	return s.sessionEvents
}
func (s *Service) SessionEventStore() sessionevents.Store {
	if store, ok := s.store.(interface{ SessionEventStore() sessionevents.Store }); ok {
		return store.SessionEventStore()
	}
	return sessionevents.NewBroker()
}
