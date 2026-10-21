package microvm

import (
	"errors"
	"time"

	sqlitepersistence "github.com/stepandel/cantelop/fire-fuse/runtime/persistence/sqlite"
)

func (s *SQLiteStore) AcquireSessionWorkLease(value SessionWorkLease) (SessionWorkLease, bool, error) {
	if err := value.Validate(); err != nil {
		return SessionWorkLease{}, false, err
	}
	stored, created, err := s.workLeases.Acquire(encodeWorkLease(value))
	if err != nil {
		return SessionWorkLease{}, false, err
	}
	return decodeWorkLease(stored), created, nil
}

func (s *SQLiteStore) RenewSessionWorkLease(sessionID, leaseID string, expiresAt, at time.Time) (SessionWorkLease, error) {
	stored, err := s.workLeases.Renew(sessionID, leaseID, expiresAt, at)
	if errors.Is(err, sqlitepersistence.ErrSessionWorkLeaseNotFound) {
		return SessionWorkLease{}, ErrWorkLeaseNotFound
	}
	if err != nil {
		return SessionWorkLease{}, err
	}
	value := decodeWorkLease(stored)
	return value, value.Validate()
}

func (s *SQLiteStore) ReleaseSessionWorkLease(sessionID, leaseID string) (bool, error) {
	return s.workLeases.Release(sessionID, leaseID)
}

func (s *SQLiteStore) ListSessionWorkLeases(sessionID string) ([]SessionWorkLease, error) {
	stored, err := s.workLeases.List(sessionID)
	if err != nil {
		return nil, err
	}
	values := make([]SessionWorkLease, len(stored))
	for i, value := range stored {
		values[i] = decodeWorkLease(value)
		if err := values[i].Validate(); err != nil {
			return nil, err
		}
	}
	return values, nil
}

func (s *SQLiteStore) ListAllSessionWorkLeases() ([]SessionWorkLease, error) {
	stored, err := s.workLeases.ListAll()
	if err != nil {
		return nil, err
	}
	values := make([]SessionWorkLease, len(stored))
	for i, value := range stored {
		values[i] = decodeWorkLease(value)
		if err := values[i].Validate(); err != nil {
			return nil, err
		}
	}
	return values, nil
}

func (s *SQLiteStore) DeleteExpiredSessionWorkLeases(at time.Time) ([]string, error) {
	return s.workLeases.DeleteExpired(at)
}

func encodeWorkLease(value SessionWorkLease) sqlitepersistence.SessionWorkLease {
	return sqlitepersistence.SessionWorkLease{
		SessionID: value.SessionID, LeaseID: value.LeaseID, ExpiresAt: value.ExpiresAt,
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}
}

func decodeWorkLease(value sqlitepersistence.SessionWorkLease) SessionWorkLease {
	return SessionWorkLease{
		SessionID: value.SessionID, LeaseID: value.LeaseID, ExpiresAt: value.ExpiresAt,
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}
}
