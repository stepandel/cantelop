package firecracker

import (
	"context"
	"errors"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/model"
	"github.com/stepandel/cantelop/fire-fuse/runtime/runtimeprovider"
	"github.com/stepandel/cantelop/fire-fuse/runtime/session"
)

type lifecycle interface {
	CreateSessionAndStart(context.Context, string, string, int) (model.Session, error)
	OpenSessionAndStart(context.Context, string, string, int, string) (model.Session, error)
	GetSession(string) (model.Session, error)
	ListSessions(string) ([]model.Session, error)
	ResolveSessionRoute(context.Context, string) (model.SessionRoute, error)
	TerminateSession(context.Context, string) (model.Session, error)
	AcquireSessionWorkLease(string, string, time.Duration) (model.SessionWorkLease, error)
	RenewSessionWorkLease(string, string, time.Duration) (model.SessionWorkLease, error)
	ReleaseSessionWorkLease(string, string) (model.Session, error)
	GetWorkspaceConfig(string) (model.WorkspaceRuntimeConfig, error)
}

// Provider adapts the current Firecracker-backed lifecycle service to the
// provider-neutral Session port.
type Provider struct {
	lifecycle lifecycle
}

// New constructs the Firecracker Session provider.
func New(lifecycle lifecycle) (*Provider, error) {
	if lifecycle == nil {
		return nil, errors.New("Firecracker lifecycle is required")
	}
	return &Provider{lifecycle: lifecycle}, nil
}

func (p *Provider) Create(ctx context.Context, request runtimeprovider.CreateRequest) (session.Session, error) {
	var sessionRecord model.Session
	var err error
	if request.KeyHash == "" {
		sessionRecord, err = p.lifecycle.CreateSessionAndStart(
			ctx, request.ID, request.WorkspaceID, request.KeepAliveSeconds,
		)
	} else {
		sessionRecord, err = p.lifecycle.OpenSessionAndStart(
			ctx, request.ID, request.WorkspaceID, request.KeepAliveSeconds, request.KeyHash,
		)
	}
	if err != nil {
		return session.Session{}, err
	}
	return MapSession(sessionRecord)
}

func (p *Provider) Get(_ context.Context, id string) (session.Session, error) {
	sessionRecord, err := p.lifecycle.GetSession(id)
	if err != nil {
		return session.Session{}, err
	}
	return MapSession(sessionRecord)
}

func (p *Provider) List(_ context.Context, workspaceID string) ([]session.Session, error) {
	sessionRecords, err := p.lifecycle.ListSessions(workspaceID)
	if err != nil {
		return nil, err
	}
	mapped := make([]session.Session, len(sessionRecords))
	for index := range sessionRecords {
		mapped[index], err = MapSession(sessionRecords[index])
		if err != nil {
			return nil, err
		}
	}
	return mapped, nil
}

func (p *Provider) Route(ctx context.Context, id string) (session.Route, error) {
	route, err := p.lifecycle.ResolveSessionRoute(ctx, id)
	if err != nil {
		return session.Route{}, err
	}
	config, err := p.lifecycle.GetWorkspaceConfig(route.Session.WorkspaceID)
	if err != nil {
		return session.Route{}, err
	}
	return MapRoute(route, config.SessionRuntime.Runtime.InternalPort)
}

func (p *Provider) Terminate(ctx context.Context, id string) (session.Session, error) {
	sessionRecord, err := p.lifecycle.TerminateSession(ctx, id)
	if err != nil {
		return session.Session{}, err
	}
	return MapSession(sessionRecord)
}

func (p *Provider) AcquireWorkLease(_ context.Context, sessionID, leaseID string, ttl time.Duration) (session.WorkLease, error) {
	value, err := p.lifecycle.AcquireSessionWorkLease(sessionID, leaseID, ttl)
	if err != nil {
		return session.WorkLease{}, err
	}
	return mapWorkLease(value), nil
}

func (p *Provider) RenewWorkLease(_ context.Context, sessionID, leaseID string, ttl time.Duration) (session.WorkLease, error) {
	value, err := p.lifecycle.RenewSessionWorkLease(sessionID, leaseID, ttl)
	if err != nil {
		return session.WorkLease{}, err
	}
	return mapWorkLease(value), nil
}

func (p *Provider) ReleaseWorkLease(_ context.Context, sessionID, leaseID string) (session.Session, error) {
	value, err := p.lifecycle.ReleaseSessionWorkLease(sessionID, leaseID)
	if err != nil {
		return session.Session{}, err
	}
	return MapSession(value)
}

func mapWorkLease(value model.SessionWorkLease) session.WorkLease {
	return session.WorkLease{
		SessionID: value.SessionID, LeaseID: value.LeaseID, ExpiresAt: value.ExpiresAt,
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}
}

var _ runtimeprovider.Provider = (*Provider)(nil)

func (p *Provider) PinnedRoute(_ context.Context, id, sandboxID string) (session.Route, error) {
	pinned, ok := p.lifecycle.(interface {
		ResolvePinnedSessionRoute(string, string) (model.SessionRoute, error)
	})
	if !ok {
		return session.Route{}, errors.New("pinned routing unavailable")
	}
	route, err := pinned.ResolvePinnedSessionRoute(id, sandboxID)
	if err != nil {
		return session.Route{}, err
	}
	config, err := p.lifecycle.GetWorkspaceConfig(route.Session.WorkspaceID)
	if err != nil {
		return session.Route{}, err
	}
	return MapRoute(route, config.SessionRuntime.Runtime.InternalPort)
}
