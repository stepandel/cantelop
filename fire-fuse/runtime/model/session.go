package model

import (
	"errors"
	"time"
)

var (
	ErrInvalidSession         = errors.New("invalid session")
	ErrSessionNotFound        = errors.New("session not found")
	ErrSessionConflict        = errors.New("session ID already used for a different Workspace")
	ErrSessionStorageQuota    = errors.New("Session storage quota exceeded")
	ErrReleaseAdmissionClosed = errors.New("App release admission is closed")
)

// Session is the public runtime identity. Internal Sandbox recovery
// and ownership details deliberately do not cross this contract.
type Session struct {
	ID               string `json:"id"`
	WorkspaceID      string `json:"workspace_id"`
	BuildID          string `json:"build_id"`
	KeepAliveSeconds int    `json:"keep_alive_seconds"`
	// State is derived from the currently bound Sandbox. It is not persisted:
	// an idle Session remains reusable and becomes active when a new Sandbox is
	// allocated for the same identity.
	State     State           `json:"state"`
	Startup   *SessionStartup `json:"startup,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// SessionStartup is derived from the backing Sandbox when the record
// is read; it is never persisted with the public identity.
type SessionStartup struct {
	CompletedAt time.Time     `json:"completed_at"`
	Duration    time.Duration `json:"duration"`
}

// SessionRoute adds the private network endpoint and exact Sandbox identity
// needed by host-local coordinators. Host ownership remains private.
type SessionRoute struct {
	Session   Session
	SandboxID string
	Network   NetworkAssignment
}

func (r Session) Validate() error {
	if ValidateSessionID(r.ID) != nil ||
		!workspaceIDPattern.MatchString(r.WorkspaceID) ||
		r.KeepAliveSeconds < MinSessionKeepAliveSeconds || r.KeepAliveSeconds > MaxSessionKeepAliveSeconds ||
		r.CreatedAt.IsZero() || r.UpdatedAt.IsZero() || r.UpdatedAt.Before(r.CreatedAt) {
		return ErrInvalidSession
	}
	if err := ValidateBuildID(r.BuildID); err != nil {
		return errors.Join(ErrInvalidSession, err)
	}
	switch r.State {
	case "", StateActive, StateIdle:
		return nil
	default:
		return ErrInvalidSession
	}
}
