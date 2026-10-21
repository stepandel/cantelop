package microvm

import (
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/model"
)

var (
	ErrInvalidWorkLease  = model.ErrInvalidWorkLease
	ErrWorkLeaseNotFound = model.ErrWorkLeaseNotFound
)

const (
	MinSessionWorkLeaseTTL = model.MinSessionWorkLeaseTTL
	MaxSessionWorkLeaseTTL = model.MaxSessionWorkLeaseTTL
)

type SessionWorkLease = model.SessionWorkLease

type SessionWorkLeaseStore interface {
	AcquireSessionWorkLease(SessionWorkLease) (SessionWorkLease, bool, error)
	RenewSessionWorkLease(string, string, time.Time, time.Time) (SessionWorkLease, error)
	ReleaseSessionWorkLease(string, string) (bool, error)
	ListSessionWorkLeases(string) ([]SessionWorkLease, error)
	ListAllSessionWorkLeases() ([]SessionWorkLease, error)
	DeleteExpiredSessionWorkLeases(time.Time) ([]string, error)
}
