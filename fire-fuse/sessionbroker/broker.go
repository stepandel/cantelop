package sessionbroker

import (
	"context"
	"errors"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/controlclient"
	"github.com/stepandel/cantelop/fire-fuse/runtime/session"
	"github.com/stepandel/cantelop/internal/resourceid"
)

// Control is the low-level Session capability supplied by the host-local
// control-plane client.
type Control interface {
	GetSession(context.Context, string) (controlclient.Session, error)
	GetSessionRoute(context.Context, string) (session.Route, error)
	CreateSession(context.Context, controlclient.CreateSessionSpec) (controlclient.Session, error)
	TerminateSession(context.Context, string) (controlclient.Session, error)
	WorkControl
}

// WorkControl holds and releases expiring Session work leases.
type WorkControl interface {
	AcquireSessionWorkLease(context.Context, string, string, time.Duration) (controlclient.WorkLease, error)
	RenewSessionWorkLease(context.Context, string, string, time.Duration) (controlclient.WorkLease, error)
	ReleaseSessionWorkLease(context.Context, string, string) (controlclient.Session, error)
}

// WorkLifecycle protects one Session Sandbox while a coordinator owns actor
// work, independently of individual message delivery requests.
type WorkLifecycle interface {
	AcquireWork(context.Context, string, string, time.Duration) (controlclient.WorkLease, error)
	RenewWork(context.Context, string, string, time.Duration) (controlclient.WorkLease, error)
	ReleaseWork(context.Context, string, string) (controlclient.Session, error)
	Route(context.Context, string) (session.Route, error)
}

type CreateSpec struct {
	ID               string
	WorkspaceID      string
	KeepAliveSeconds int
}

type KeyedSpec struct {
	WorkspaceID      string
	WorkspaceSlug    string
	Key              string
	KeepAliveSeconds int
}

type BoundSpec struct {
	WorkspaceID      string
	WorkspaceSlug    string
	KeyHash          string
	KeepAliveSeconds int
}

type LogicalSpec struct {
	AppID            string
	WorkspaceID      string
	LogicalID        string
	KeepAliveSeconds int
}

// Broker owns Session identity, keyed get-or-create policy, and lifecycle
// operations. It deliberately does not decide App/Workspace authorization.
type Broker struct {
	control Control
}

func New(control Control) (*Broker, error) {
	if control == nil {
		return nil, errors.New("Session control is required")
	}
	return &Broker{control: control}, nil
}

func (b *Broker) Create(ctx context.Context, spec CreateSpec) (controlclient.Session, error) {
	return b.control.CreateSession(ctx, controlclient.CreateSessionSpec{
		ID: spec.ID, WorkspaceID: spec.WorkspaceID, KeepAliveSeconds: spec.KeepAliveSeconds,
	})
}

func (b *Broker) OpenKeyed(ctx context.Context, spec KeyedSpec) (controlclient.Session, error) {
	return b.OpenBound(ctx, BoundSpec{
		WorkspaceID: spec.WorkspaceID, WorkspaceSlug: spec.WorkspaceSlug,
		KeyHash: KeyHash(spec.WorkspaceID, spec.Key), KeepAliveSeconds: spec.KeepAliveSeconds,
	})
}

func (b *Broker) OpenBound(ctx context.Context, spec BoundSpec) (controlclient.Session, error) {
	id, err := NewID(spec.WorkspaceSlug)
	if err != nil {
		return controlclient.Session{}, err
	}
	return b.control.CreateSession(ctx, controlclient.CreateSessionSpec{
		ID: id, WorkspaceID: spec.WorkspaceID, KeepAliveSeconds: spec.KeepAliveSeconds,
		KeyHash: spec.KeyHash,
	})
}

func (b *Broker) OpenLogical(ctx context.Context, spec LogicalSpec) (controlclient.Session, error) {
	return b.control.CreateSession(ctx, controlclient.CreateSessionSpec{
		ID: LogicalID(spec.AppID, spec.LogicalID), WorkspaceID: spec.WorkspaceID,
		KeepAliveSeconds: spec.KeepAliveSeconds,
		KeyHash:          LogicalKeyHash(spec.WorkspaceID, spec.LogicalID),
	})
}

func (b *Broker) Get(ctx context.Context, id string) (controlclient.Session, error) {
	return b.control.GetSession(ctx, id)
}

// ResolvePresented accepts the logical actor identity used by the SDK and
// falls back to a concrete Session ID for direct lifecycle operations.
func (b *Broker) ResolvePresented(ctx context.Context, appID, presentedID string) (controlclient.Session, error) {
	candidates := []string{LogicalID(appID, presentedID)}
	if resourceid.ValidSessionID(presentedID) && candidates[0] != presentedID {
		candidates = append(candidates, presentedID)
	}
	var lastErr error
	for _, candidate := range candidates {
		active, err := b.control.GetSession(ctx, candidate)
		if err == nil {
			return active, nil
		}
		lastErr = err
	}
	return controlclient.Session{}, lastErr
}

func (b *Broker) Route(ctx context.Context, id string) (session.Route, error) {
	return b.control.GetSessionRoute(ctx, id)
}

func (b *Broker) Release(ctx context.Context, id string) (controlclient.Session, error) {
	return b.control.TerminateSession(ctx, id)
}

func (b *Broker) AcquireWork(ctx context.Context, sessionID, leaseID string, ttl time.Duration) (controlclient.WorkLease, error) {
	return b.control.AcquireSessionWorkLease(ctx, sessionID, leaseID, ttl)
}

func (b *Broker) RenewWork(ctx context.Context, sessionID, leaseID string, ttl time.Duration) (controlclient.WorkLease, error) {
	return b.control.RenewSessionWorkLease(ctx, sessionID, leaseID, ttl)
}

func (b *Broker) ReleaseWork(ctx context.Context, sessionID, leaseID string) (controlclient.Session, error) {
	return b.control.ReleaseSessionWorkLease(ctx, sessionID, leaseID)
}

var _ WorkLifecycle = (*Broker)(nil)
