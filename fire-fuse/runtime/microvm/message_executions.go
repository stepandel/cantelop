package microvm

import "github.com/stepandel/cantelop/fire-fuse/runtime/messagequeue"

type executionStore interface {
	SaveExecution(messagequeue.Execution) (messagequeue.Execution, error)
	ListExecutions() ([]messagequeue.Execution, error)
}

func (s *SQLiteStore) SaveExecution(e messagequeue.Execution) (messagequeue.Execution, error) {
	return s.messageDeliveries.SaveExecution(e, e.UpdatedAt)
}
func (s *SQLiteStore) ListExecutions() ([]messagequeue.Execution, error) {
	return s.messageDeliveries.ListExecutions()
}
func (s *Service) SaveExecution(e messagequeue.Execution) (messagequeue.Execution, error) {
	store, ok := s.store.(executionStore)
	if !ok {
		return e, ErrStateStore
	}
	e.UpdatedAt = s.now().UTC()
	return store.SaveExecution(e)
}
func (s *Service) ListExecutions() ([]messagequeue.Execution, error) {
	store, ok := s.store.(executionStore)
	if !ok {
		return nil, ErrStateStore
	}
	return store.ListExecutions()
}

// ResolvePinnedSessionRoute never creates or reactivates a Sandbox during recovery.
func (s *Service) ResolvePinnedSessionRoute(id, sandboxID string) (SessionRoute, error) {
	unlock := s.sessionLocks.lock(id)
	defer unlock()
	machine, err := s.store.GetSandboxForSession(id)
	if err != nil {
		return SessionRoute{}, err
	}
	if machine.ID != sandboxID || machine.State != StateRunning {
		return SessionRoute{}, ErrInvalidTransition
	}
	record, err := s.store.GetSession(id)
	if err != nil {
		return SessionRoute{}, err
	}
	record.State = StateActive
	return SessionRoute{Session: record, SandboxID: machine.ID, Network: machine.Network}, nil
}
