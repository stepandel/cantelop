package microvm

import (
	"regexp"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/model"
)

var (
	ErrInvalidSession         = model.ErrInvalidSession
	ErrSessionNotFound        = model.ErrSessionNotFound
	ErrSessionConflict        = model.ErrSessionConflict
	ErrSessionStorageQuota    = model.ErrSessionStorageQuota
	ErrReleaseAdmissionClosed = model.ErrReleaseAdmissionClosed
	sessionIDPattern          = regexp.MustCompile(`^ses_[A-Za-z0-9][A-Za-z0-9_-]{0,59}$`)
	sessionKeyHashPattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type Session = model.Session

type SessionStartup = model.SessionStartup

type SessionRoute = model.SessionRoute

// SessionStore persists the public Session identity and its private
// Sandbox implementation atomically.
type SessionStore interface {
	CreateSession(Session, Sandbox, string) (Session, bool, error)
	GetSession(sessionID string) (Session, error)
	GetSessionByKey(workspaceID, keyHash string) (Session, error)
	ListSessions(workspaceID string) ([]Session, error)
	UpdateSessionKeepAlive(sessionID, workspaceID string, keepAliveSeconds int, at time.Time) (Session, error)
	GetSandboxForSession(sessionID string) (Sandbox, error)
	FenceSessionStop(sessionID string, sandbox Sandbox, at time.Time) error
	ReplaceSessionSandbox(sessionID string, sandbox Sandbox, at time.Time) (Session, error)
}
