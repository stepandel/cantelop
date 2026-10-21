package microvm

import (
	"errors"

	sqlitepersistence "github.com/stepandel/cantelop/fire-fuse/runtime/persistence/sqlite"
)

var (
	ErrRequestEventConflict = sqlitepersistence.ErrConflict
	ErrRequestEventStore    = errors.New("request event store is unavailable")
)

// RequestEventStore persists individual reverse-proxy requests and serves
// bounded, newest-first pages.
type RequestEventStore interface {
	RecordRequestEvent(RequestEvent) (RequestEvent, bool, error)
	ListRequestEvents(RequestEventQuery) (RequestEventPage, error)
}
