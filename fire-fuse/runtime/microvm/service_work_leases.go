package microvm

import (
	"errors"
	"fmt"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/model"
)

func (s *Service) AcquireSessionWorkLease(sessionID, leaseID string, ttl time.Duration) (SessionWorkLease, error) {
	return s.AcquirePinnedSessionWorkLease(sessionID, "", leaseID, ttl)
}

func (s *Service) AcquirePinnedSessionWorkLease(sessionID, sandboxID, leaseID string, ttl time.Duration) (SessionWorkLease, error) {
	if model.ValidateSessionID(sessionID) != nil || model.ValidateWorkLeaseID(leaseID) != nil ||
		ttl < MinSessionWorkLeaseTTL || ttl > MaxSessionWorkLeaseTTL {
		return SessionWorkLease{}, ErrInvalidWorkLease
	}
	unlock := s.sessionLocks.lock(sessionID)
	defer unlock()
	if sandboxID != "" {
		machine, err := s.store.GetSandboxForSession(sessionID)
		if err != nil {
			return SessionWorkLease{}, err
		}
		if machine.ID != sandboxID || machine.State != StateRunning {
			return SessionWorkLease{}, ErrInvalidTransition
		}
	}
	now := s.now().UTC()
	if err := s.expireSessionWorkLeasesLocked(sessionID, now); err != nil {
		return SessionWorkLease{}, err
	}
	machine, err := s.store.GetSandboxForSession(sessionID)
	if err != nil {
		return SessionWorkLease{}, err
	}
	if machine.State != StateRunning {
		return SessionWorkLease{}, ErrInvalidTransition
	}
	value := SessionWorkLease{
		SessionID: sessionID, LeaseID: leaseID, ExpiresAt: now.Add(ttl),
		CreatedAt: now, UpdatedAt: now,
	}
	stored, _, err := s.store.AcquireSessionWorkLease(value)
	if err != nil {
		return SessionWorkLease{}, wrapStateStoreError("acquire Session work lease", err)
	}
	if !stored.ExpiresAt.After(now) {
		if _, err := s.store.ReleaseSessionWorkLease(sessionID, leaseID); err != nil {
			return SessionWorkLease{}, wrapStateStoreError("replace expired Session work lease", err)
		}
		stored, _, err = s.store.AcquireSessionWorkLease(value)
		if err != nil {
			return SessionWorkLease{}, wrapStateStoreError("replace expired Session work lease", err)
		}
	}
	machine, err = s.store.SetIdleDeadline(machine.ID, time.Time{}, now)
	if err != nil {
		return SessionWorkLease{}, wrapStateStoreError("protect leased Session Sandbox", err)
	}
	s.scheduleSessionDeadlines(sessionID, machine)
	s.scheduleSessionWorkLease(stored)
	return stored, nil
}

func (s *Service) RenewSessionWorkLease(sessionID, leaseID string, ttl time.Duration) (SessionWorkLease, error) {
	return s.RenewPinnedSessionWorkLease(sessionID, "", leaseID, ttl)
}

func (s *Service) RenewPinnedSessionWorkLease(sessionID, sandboxID, leaseID string, ttl time.Duration) (SessionWorkLease, error) {
	if model.ValidateSessionID(sessionID) != nil || model.ValidateWorkLeaseID(leaseID) != nil ||
		ttl < MinSessionWorkLeaseTTL || ttl > MaxSessionWorkLeaseTTL {
		return SessionWorkLease{}, ErrInvalidWorkLease
	}
	unlock := s.sessionLocks.lock(sessionID)
	defer unlock()
	if sandboxID != "" {
		machine, err := s.store.GetSandboxForSession(sessionID)
		if err != nil {
			return SessionWorkLease{}, err
		}
		if machine.ID != sandboxID || machine.State != StateRunning {
			return SessionWorkLease{}, ErrInvalidTransition
		}
	}
	now := s.now().UTC()
	if err := s.expireSessionWorkLeasesLocked(sessionID, now); err != nil {
		return SessionWorkLease{}, err
	}
	stored, err := s.store.RenewSessionWorkLease(sessionID, leaseID, now.Add(ttl), now)
	if err != nil {
		return SessionWorkLease{}, err
	}
	machine, err := s.store.GetSandboxForSession(sessionID)
	if err != nil {
		return SessionWorkLease{}, err
	}
	if machine.State != StateRunning {
		return SessionWorkLease{}, ErrInvalidTransition
	}
	machine, err = s.store.SetIdleDeadline(machine.ID, time.Time{}, now)
	if err != nil {
		return SessionWorkLease{}, wrapStateStoreError("renew leased Session protection", err)
	}
	s.scheduleSessionDeadlines(sessionID, machine)
	s.scheduleSessionWorkLease(stored)
	return stored, nil
}

func (s *Service) ReleaseSessionWorkLease(sessionID, leaseID string) (Session, error) {
	return s.ReleasePinnedSessionWorkLease(sessionID, "", leaseID)
}
func (s *Service) ReleasePinnedSessionWorkLease(sessionID, sandboxID, leaseID string) (Session, error) {
	if model.ValidateSessionID(sessionID) != nil || model.ValidateWorkLeaseID(leaseID) != nil {
		return Session{}, ErrInvalidWorkLease
	}
	unlock := s.sessionLocks.lock(sessionID)
	defer unlock()
	if _, err := s.store.GetSession(sessionID); err != nil {
		return Session{}, err
	}
	released, err := s.store.ReleaseSessionWorkLease(sessionID, leaseID)
	if err != nil {
		return Session{}, wrapStateStoreError("release Session work lease", err)
	}
	if !released {
		return s.GetSession(sessionID)
	}
	s.cancelSessionWorkLeaseTimer(sessionID, leaseID)
	if sandboxID != "" {
		machine, err := s.store.GetSandboxForSession(sessionID)
		if err != nil {
			return Session{}, err
		}
		if machine.ID != sandboxID {
			return s.GetSession(sessionID)
		}
	}
	if err := s.applyUnleasedSessionDeadlineLocked(sessionID, s.now().UTC()); err != nil {
		return Session{}, err
	}
	return s.GetSession(sessionID)
}

// RestoreSessionWorkLeaseSchedules reconciles expired leases and restores
// expiry timers for leases that survived a Fire Fuse restart.
func (s *Service) RestoreSessionWorkLeaseSchedules() error {
	if err := s.expireSessionWorkLeases(s.now().UTC()); err != nil {
		return err
	}
	values, err := s.store.ListAllSessionWorkLeases()
	if err != nil {
		return wrapStateStoreError("restore Session work leases", err)
	}
	protected := make(map[string]struct{})
	for _, value := range values {
		protected[value.SessionID] = struct{}{}
		s.scheduleSessionWorkLease(value)
	}
	for sessionID := range protected {
		if err := s.restoreLeasedSessionProtection(sessionID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) restoreLeasedSessionProtection(sessionID string) error {
	unlock := s.sessionLocks.lock(sessionID)
	defer unlock()
	// The lease listing was read without this Session's lock; never protect a
	// Session whose leases were released since.
	leases, err := s.store.ListSessionWorkLeases(sessionID)
	if err != nil {
		return wrapStateStoreError("list Session work leases", err)
	}
	if len(leases) == 0 {
		return nil
	}
	machine, err := s.store.GetSandboxForSession(sessionID)
	if err != nil {
		return err
	}
	if machine.State != StateRunning || machine.IdleDeadline.IsZero() {
		return nil
	}
	machine, err = s.store.SetIdleDeadline(machine.ID, time.Time{}, s.now().UTC())
	if err != nil {
		return wrapStateStoreError("restore leased Session protection", err)
	}
	s.scheduleSessionDeadlines(sessionID, machine)
	return nil
}

// expireSessionWorkLeases deletes every expired lease, then restarts the idle
// period of each affected Session under that Session's lock. The caller must
// not hold any Session lock.
func (s *Service) expireSessionWorkLeases(now time.Time) error {
	sessionIDs, err := s.store.DeleteExpiredSessionWorkLeases(now)
	if err != nil {
		return wrapStateStoreError("expire Session work leases", err)
	}
	// Every listed Session already lost its expired leases, so one failure must
	// not skip the deadlines of the others.
	var deadlineErrors []error
	for _, sessionID := range sessionIDs {
		unlock := s.sessionLocks.lock(sessionID)
		err := s.applyUnleasedSessionDeadlineLocked(sessionID, s.now().UTC())
		unlock()
		if err != nil {
			deadlineErrors = append(deadlineErrors, err)
		}
	}
	return errors.Join(deadlineErrors...)
}

// expireSessionWorkLeasesLocked removes one locked Session's expired leases and
// restarts its idle period if that leaves it unleased. Other Sessions' leases
// expire through their own timers.
func (s *Service) expireSessionWorkLeasesLocked(sessionID string, now time.Time) error {
	values, err := s.store.ListSessionWorkLeases(sessionID)
	if err != nil {
		return wrapStateStoreError("list Session work leases", err)
	}
	expired := false
	for _, value := range values {
		if value.ExpiresAt.After(now) {
			continue
		}
		if _, err := s.store.ReleaseSessionWorkLease(sessionID, value.LeaseID); err != nil {
			return wrapStateStoreError("expire Session work leases", err)
		}
		s.cancelSessionWorkLeaseTimer(sessionID, value.LeaseID)
		expired = true
	}
	if !expired {
		return nil
	}
	return s.applyUnleasedSessionDeadlineLocked(sessionID, now)
}

func (s *Service) applyUnleasedSessionDeadlineLocked(sessionID string, now time.Time) error {
	values, err := s.store.ListSessionWorkLeases(sessionID)
	if err != nil {
		return wrapStateStoreError("list Session work leases", err)
	}
	if len(values) > 0 {
		return nil
	}
	sessionRecord, err := s.store.GetSession(sessionID)
	if err != nil {
		return err
	}
	machine, err := s.store.GetSandboxForSession(sessionID)
	if err != nil {
		return err
	}
	if machine.State != StateRunning {
		return nil
	}
	machine, err = s.store.SetIdleDeadline(machine.ID,
		now.Add(time.Duration(sessionRecord.KeepAliveSeconds)*time.Second), now)
	if err != nil {
		return wrapStateStoreError("start unleased Session idle period", err)
	}
	s.scheduleSessionDeadlines(sessionID, machine)
	return nil
}

func (s *Service) scheduleSessionWorkLease(value SessionWorkLease) {
	key := value.SessionID + "\x00" + value.LeaseID
	delay := value.ExpiresAt.Sub(s.now().UTC())
	if delay < 0 {
		delay = 0
	}
	s.workLeaseMu.Lock()
	if existing := s.workLeaseTimers[key]; existing != nil {
		existing.Stop()
	}
	var timer *time.Timer
	timer = time.AfterFunc(delay, func() {
		s.workLeaseMu.Lock()
		if s.workLeaseTimers[key] != timer {
			s.workLeaseMu.Unlock()
			return
		}
		delete(s.workLeaseTimers, key)
		s.workLeaseMu.Unlock()
		if err := s.expireSessionWorkLeases(s.now().UTC()); err != nil {
			s.scheduleSessionWorkLease(SessionWorkLease{
				SessionID: value.SessionID, LeaseID: value.LeaseID,
				ExpiresAt: s.now().UTC().Add(time.Second), CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
			})
		}
	})
	s.workLeaseTimers[key] = timer
	s.workLeaseMu.Unlock()
}

func (s *Service) cancelSessionWorkLeaseTimer(sessionID, leaseID string) {
	key := sessionID + "\x00" + leaseID
	s.workLeaseMu.Lock()
	if timer := s.workLeaseTimers[key]; timer != nil {
		timer.Stop()
		delete(s.workLeaseTimers, key)
	}
	s.workLeaseMu.Unlock()
}

func (s *Service) revokeSessionWorkLeasesLocked(sessionID string) error {
	values, err := s.store.ListSessionWorkLeases(sessionID)
	if err != nil {
		return fmt.Errorf("list Session work leases for termination: %w", err)
	}
	for _, value := range values {
		if _, err := s.store.ReleaseSessionWorkLease(sessionID, value.LeaseID); err != nil {
			return fmt.Errorf("revoke Session work lease: %w", err)
		}
		s.cancelSessionWorkLeaseTimer(sessionID, value.LeaseID)
	}
	return nil
}
