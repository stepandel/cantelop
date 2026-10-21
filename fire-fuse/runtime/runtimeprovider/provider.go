// Package runtimeprovider defines Cantelop's provider-neutral Session port.
package runtimeprovider

import (
	"context"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/session"
)

// CreateRequest identifies one idempotent Workspace Session.
type CreateRequest struct {
	ID               string
	WorkspaceID      string
	KeepAliveSeconds int
	KeyHash          string
}

// Provider owns stable Session identities and their replaceable Sandbox
// allocations, and resolves provider-selected routes.
type Provider interface {
	Create(context.Context, CreateRequest) (session.Session, error)
	Get(context.Context, string) (session.Session, error)
	List(context.Context, string) ([]session.Session, error)
	Route(context.Context, string) (session.Route, error)
	Terminate(context.Context, string) (session.Session, error)
	AcquireWorkLease(context.Context, string, string, time.Duration) (session.WorkLease, error)
	RenewWorkLease(context.Context, string, string, time.Duration) (session.WorkLease, error)
	ReleaseWorkLease(context.Context, string, string) (session.Session, error)
}
