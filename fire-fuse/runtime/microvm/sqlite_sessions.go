package microvm

import (
	"errors"
	"time"

	sqlitepersistence "github.com/stepandel/cantelop/fire-fuse/runtime/persistence/sqlite"
)

var _ SessionStore = (*SQLiteStore)(nil)

func (s *SQLiteStore) CreateSession(instance Session, sandbox Sandbox, keyHash string) (Session, bool, error) {
	existing, err := s.sessions.Get(instance.ID)
	if err == nil {
		if existing.WorkspaceID != instance.WorkspaceID ||
			(keyHash == "" && existing.KeepAliveSeconds != instance.KeepAliveSeconds) {
			return existing, false, ErrSessionConflict
		}
		return existing, false, nil
	}
	if !errors.Is(err, ErrSessionNotFound) {
		return Session{}, false, err
	}
	if keyHash != "" {
		existing, err = s.sessions.GetByKey(instance.WorkspaceID, keyHash)
		if err == nil {
			return existing, false, nil
		}
		if !errors.Is(err, ErrSessionNotFound) {
			return Session{}, false, err
		}
	}
	workspace, err := s.workspaces.Get(instance.WorkspaceID)
	if err != nil {
		return Session{}, false, err
	}
	sessionRuntime, err := s.catalog.GetSessionRuntime(workspace.AppID)
	if err != nil {
		return Session{}, false, err
	}
	if err := validateSessionBinding(instance, sandbox, workspace, sessionRuntime); err != nil {
		return Session{}, false, err
	}
	record, err := encodeSandboxRecord(sandbox)
	if err != nil {
		return Session{}, false, err
	}
	return s.sessions.Create(instance, sqlitepersistence.RuntimeBinding{
		ID: sandbox.ID, State: sandbox.State, Record: record,
	}, keyHash)
}

func (s *SQLiteStore) GetSession(id string) (Session, error) {
	return s.sessions.Get(id)
}

func (s *SQLiteStore) GetSessionByKey(workspaceID, keyHash string) (Session, error) {
	return s.sessions.GetByKey(workspaceID, keyHash)
}

func (s *SQLiteStore) ListSessions(workspaceID string) ([]Session, error) {
	return s.sessions.List(workspaceID)
}

func (s *SQLiteStore) UpdateSessionKeepAlive(
	sessionID, workspaceID string,
	keepAliveSeconds int,
	at time.Time,
) (Session, error) {
	return s.sessions.UpdateKeepAlive(sessionID, workspaceID, keepAliveSeconds, at)
}

func (s *SQLiteStore) GetSandboxForSession(sessionID string) (Sandbox, error) {
	binding, err := s.sessions.GetBinding(sessionID)
	if err != nil {
		return Sandbox{}, err
	}
	return decodeSQLiteSandbox(binding.ID, binding.State, binding.Record)
}

func (s *SQLiteStore) ReplaceSessionSandbox(sessionID string, sandbox Sandbox, at time.Time) (Session, error) {
	instance, err := s.sessions.Get(sessionID)
	if err != nil {
		return Session{}, err
	}
	workspace, err := s.workspaces.Get(instance.WorkspaceID)
	if err != nil {
		return Session{}, err
	}
	sessionRuntime, err := s.catalog.GetSessionRuntime(workspace.AppID)
	if err != nil {
		return Session{}, err
	}
	instance.BuildID = sandbox.BuildID
	instance.UpdatedAt = at
	if err := validateSessionBinding(instance, sandbox, workspace, sessionRuntime); err != nil {
		return Session{}, err
	}
	record, err := encodeSandboxRecord(sandbox)
	if err != nil {
		return Session{}, err
	}
	return s.sessions.ReplaceBinding(sessionID, sandbox.BuildID, sqlitepersistence.RuntimeBinding{
		ID: sandbox.ID, State: sandbox.State, Record: record,
	}, at)
}

func validateSessionBinding(
	instance Session,
	sandbox Sandbox,
	workspace Workspace,
	sessionRuntime AppSessionRuntime,
) error {
	profile, profileErr := ResolveSandboxProfile(sessionRuntime.Runtime.Profile)
	if err := instance.Validate(); err != nil {
		return err
	}
	if err := validatePersistedSandbox(sandbox); err != nil {
		return err
	}
	if workspace.ArchivedAt != nil || instance.WorkspaceID != workspace.ID ||
		instance.BuildID != sessionRuntime.ActiveBuildID || sandbox.BuildID != instance.BuildID ||
		sandbox.AppID != workspace.AppID || sandbox.WorkspaceID != workspace.ID ||
		profileErr != nil || sandbox.Profile != profile.Name || sandbox.VCPUs != profile.VCPUs ||
		sandbox.MemoryMiB != profile.MemoryMiB || sandbox.CreatedAt.Before(instance.CreatedAt) {
		return errors.Join(ErrInvalidSession, ErrInvalidWorkspace)
	}
	return nil
}

func (s *SQLiteStore) FenceSessionStop(sessionID string, sandbox Sandbox, at time.Time) error {
	return s.messageDeliveries.FenceSessionStop(sessionID, sandbox.ID, func(stored sqlitepersistence.SandboxRecord) (sqlitepersistence.SandboxRecord, time.Time, error) {
		current, err := decodeSQLiteSandbox(stored.ID, stored.State, stored.Record)
		if err != nil {
			return stored, at, err
		}
		if current.State != StateTerminated {
			current.State = StateStopping
		}
		current.UpdatedAt = at
		record, err := encodeSandboxRecord(current)
		return sqlitepersistence.SandboxRecord{ID: current.ID, State: current.State, Record: record}, at, err
	}, at)
}
