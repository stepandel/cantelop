package sessionbroker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/controlclient"
	"github.com/stepandel/cantelop/fire-fuse/runtime/session"
)

func TestBrokerOwnsSessionOpeningPolicy(t *testing.T) {
	control := &fakeControl{}
	broker, err := New(control)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := broker.Create(context.Background(), CreateSpec{
		ID: "ses_explicit", WorkspaceID: "wsp_a", KeepAliveSeconds: 10,
	}); err != nil {
		t.Fatal(err)
	}
	if got := control.lastCreate; got.ID != "ses_explicit" || got.KeyHash != "" {
		t.Fatalf("Create() spec = %#v", got)
	}
	if _, err := broker.OpenKeyed(context.Background(), KeyedSpec{
		WorkspaceID: "wsp_a", WorkspaceSlug: "primary", Key: "thread", KeepAliveSeconds: 20,
	}); err != nil {
		t.Fatal(err)
	}
	if got := control.lastCreate; got.WorkspaceID != "wsp_a" || got.KeyHash != KeyHash("wsp_a", "thread") {
		t.Fatalf("OpenKeyed() spec = %#v", got)
	}
	if _, err := broker.OpenLogical(context.Background(), LogicalSpec{
		AppID: "app_a", WorkspaceID: "wsp_a", LogicalID: "thread", KeepAliveSeconds: 30,
	}); err != nil {
		t.Fatal(err)
	}
	if got := control.lastCreate; got.ID != LogicalID("app_a", "thread") ||
		got.KeyHash != LogicalKeyHash("wsp_a", "thread") {
		t.Fatalf("OpenLogical() spec = %#v", got)
	}
}

func TestBrokerResolvesLogicalThenExplicitIdentity(t *testing.T) {
	notFound := errors.New("not found")
	control := &fakeControl{sessions: map[string]controlclient.Session{
		"ses_explicit": {ID: "ses_explicit", WorkspaceID: "wsp_a"},
	}, missing: notFound}
	broker, err := New(control)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := broker.ResolvePresented(context.Background(), "app_a", "ses_explicit")
	if err != nil || resolved.ID != "ses_explicit" {
		t.Fatalf("ResolvePresented() = %#v, %v", resolved, err)
	}
	wantLogical := LogicalID("app_a", "thread")
	control.sessions[wantLogical] = controlclient.Session{ID: wantLogical, WorkspaceID: "wsp_a"}
	resolved, err = broker.ResolvePresented(context.Background(), "app_a", "thread")
	if err != nil || resolved.ID != wantLogical {
		t.Fatalf("ResolvePresented(logical) = %#v, %v", resolved, err)
	}
}

func TestNewRequiresControl(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Fatal("New() error = nil")
	}
}

func TestBrokerRoutesSessionWorkLifecycle(t *testing.T) {
	control := &fakeControl{}
	broker, err := New(control)
	if err != nil {
		t.Fatal(err)
	}
	leaseID := "lease_0123456789abcdef0123456789abcdef"
	if _, err := broker.AcquireWork(context.Background(), "ses_actor", leaseID, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := broker.RenewWork(context.Background(), "ses_actor", leaseID, 2*time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := broker.ReleaseWork(context.Background(), "ses_actor", leaseID); err != nil {
		t.Fatal(err)
	}
	if len(control.operations) != 3 || control.operations[0] != "acquire" || control.operations[2] != "release" {
		t.Fatalf("work operations = %#v", control.operations)
	}
}

type fakeControl struct {
	lastCreate controlclient.CreateSessionSpec
	sessions   map[string]controlclient.Session
	missing    error
	operations []string
}

func (f *fakeControl) AcquireSessionWorkLease(context.Context, string, string, time.Duration) (controlclient.WorkLease, error) {
	f.operations = append(f.operations, "acquire")
	return controlclient.WorkLease{}, nil
}

func (f *fakeControl) RenewSessionWorkLease(context.Context, string, string, time.Duration) (controlclient.WorkLease, error) {
	f.operations = append(f.operations, "renew")
	return controlclient.WorkLease{}, nil
}

func (f *fakeControl) ReleaseSessionWorkLease(context.Context, string, string) (controlclient.Session, error) {
	f.operations = append(f.operations, "release")
	return controlclient.Session{}, nil
}

func (f *fakeControl) GetSession(_ context.Context, id string) (controlclient.Session, error) {
	if value, ok := f.sessions[id]; ok {
		return value, nil
	}
	return controlclient.Session{}, f.missing
}

func (f *fakeControl) GetSessionRoute(context.Context, string) (session.Route, error) {
	return session.Route{}, nil
}

func (f *fakeControl) CreateSession(_ context.Context, spec controlclient.CreateSessionSpec) (controlclient.Session, error) {
	f.lastCreate = spec
	return controlclient.Session{ID: spec.ID, WorkspaceID: spec.WorkspaceID}, nil
}

func (f *fakeControl) TerminateSession(context.Context, string) (controlclient.Session, error) {
	return controlclient.Session{}, nil
}
