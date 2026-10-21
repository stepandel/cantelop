package firecracker

import (
	"context"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/model"
	"github.com/stepandel/cantelop/fire-fuse/runtime/runtimeprovider"
	"github.com/stepandel/cantelop/fire-fuse/runtime/session"
)

type lifecycleStub struct {
	createRequest runtimeprovider.CreateRequest
	sessionRecord model.Session
	route         model.SessionRoute
	config        model.WorkspaceRuntimeConfig
	workLease     model.SessionWorkLease
}

func (s *lifecycleStub) CreateSessionAndStart(
	_ context.Context, id, workspaceID string, keepAliveSeconds int,
) (model.Session, error) {
	s.createRequest = runtimeprovider.CreateRequest{
		ID: id, WorkspaceID: workspaceID, KeepAliveSeconds: keepAliveSeconds,
	}
	return s.sessionRecord, nil
}
func (s *lifecycleStub) OpenSessionAndStart(
	_ context.Context, id, workspaceID string, keepAliveSeconds int, keyHash string,
) (model.Session, error) {
	s.createRequest = runtimeprovider.CreateRequest{
		ID: id, WorkspaceID: workspaceID, KeepAliveSeconds: keepAliveSeconds, KeyHash: keyHash,
	}
	return s.sessionRecord, nil
}
func (s *lifecycleStub) GetSession(string) (model.Session, error) {
	return s.sessionRecord, nil
}
func (s *lifecycleStub) ListSessions(string) ([]model.Session, error) {
	return []model.Session{s.sessionRecord}, nil
}
func (s *lifecycleStub) ResolveSessionRoute(context.Context, string) (model.SessionRoute, error) {
	return s.route, nil
}
func (s *lifecycleStub) TerminateSession(context.Context, string) (model.Session, error) {
	return s.sessionRecord, nil
}
func (s *lifecycleStub) AcquireSessionWorkLease(string, string, time.Duration) (model.SessionWorkLease, error) {
	return s.workLease, nil
}
func (s *lifecycleStub) RenewSessionWorkLease(string, string, time.Duration) (model.SessionWorkLease, error) {
	return s.workLease, nil
}
func (s *lifecycleStub) ReleaseSessionWorkLease(string, string) (model.Session, error) {
	return s.sessionRecord, nil
}
func (s *lifecycleStub) GetWorkspaceConfig(string) (model.WorkspaceRuntimeConfig, error) {
	return s.config, nil
}
func TestProviderCreatesAndRoutesFirecrackerRuntime(t *testing.T) {
	sessionRecord := model.Session{
		ID: "ses_editor", WorkspaceID: "wsp_editor", KeepAliveSeconds: 300, State: model.StateActive,
	}
	lifecycle := &lifecycleStub{
		sessionRecord: sessionRecord,
		route: model.SessionRoute{
			Session:   sessionRecord,
			SandboxID: "sbx-0123456789abcdef0123456789abcdef",
			Network:   model.NetworkAssignment{GuestIP: "172.16.0.2"},
		},
		config: model.WorkspaceRuntimeConfig{
			SessionRuntime: model.AppSessionRuntime{Runtime: model.AppRuntimeSpec{InternalPort: 3000}},
		},
	}
	provider, err := New(lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	request := runtimeprovider.CreateRequest{
		ID: "ses_editor", WorkspaceID: "wsp_editor", KeepAliveSeconds: 300,
	}
	created, err := provider.Create(context.Background(), request)
	if err != nil || created.State != session.StateActive || lifecycle.createRequest != request {
		t.Fatalf("Create() = %+v, %v; request = %+v", created, err, lifecycle.createRequest)
	}
	route, err := provider.Route(context.Background(), created.ID)
	if err != nil || route.SandboxID != "sbx-0123456789abcdef0123456789abcdef" ||
		route.Endpoint.Scheme != "http" || route.Endpoint.Authority != "172.16.0.2:3000" {
		t.Fatalf("Route() = %+v, %v", route, err)
	}
}

func TestProviderOpensKeyedSession(t *testing.T) {
	lifecycle := &lifecycleStub{sessionRecord: model.Session{
		ID: "ses_editor", WorkspaceID: "wsp_editor", KeepAliveSeconds: 300, State: model.StateActive,
	}}
	provider, err := New(lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	request := runtimeprovider.CreateRequest{
		ID: "ses_proposed", WorkspaceID: "wsp_editor", KeepAliveSeconds: 300,
		KeyHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	if _, err := provider.Create(context.Background(), request); err != nil || lifecycle.createRequest != request {
		t.Fatalf("Create() error = %v; request = %+v", err, lifecycle.createRequest)
	}
}
