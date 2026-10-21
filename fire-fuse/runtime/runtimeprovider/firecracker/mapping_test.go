package firecracker

import (
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/model"
	"github.com/stepandel/cantelop/fire-fuse/runtime/session"
)

func TestMapSession(t *testing.T) {
	createdAt := time.Date(2026, time.August, 11, 12, 0, 0, 0, time.UTC)
	completedAt := createdAt.Add(125 * time.Millisecond)
	source := model.Session{
		ID: "ses_editor", WorkspaceID: "wsp_editor", BuildID: "sha256:build",
		State: model.StateActive,
		Startup: &model.SessionStartup{
			CompletedAt: completedAt,
			Duration:    125 * time.Millisecond,
		},
		CreatedAt: createdAt, UpdatedAt: completedAt,
	}

	mapped, err := MapSession(source)
	if err != nil {
		t.Fatal(err)
	}
	if mapped.ID != source.ID || mapped.WorkspaceID != source.WorkspaceID ||
		mapped.BuildID != source.BuildID || mapped.State != session.StateActive ||
		mapped.CreatedAt != createdAt || mapped.UpdatedAt != completedAt {
		t.Fatalf("mapped Session = %+v", mapped)
	}
	if mapped.Startup == nil || mapped.Startup.CompletedAt != completedAt || mapped.Startup.Duration != 125*time.Millisecond {
		t.Fatalf("mapped startup = %+v", mapped.Startup)
	}
	source.Startup.CompletedAt = time.Time{}
	if mapped.Startup.CompletedAt != completedAt {
		t.Fatal("mapped startup aliases the microVM source")
	}
}

func TestMapSessionRejectsUnknownState(t *testing.T) {
	for _, state := range []model.State{
		model.StateStarting,
		model.StateStopping,
		model.StateFailed,
		model.State("paused"),
	} {
		if _, err := MapSession(model.Session{State: state}); err == nil {
			t.Fatalf("state %q was accepted", state)
		}
	}
}

func TestMapRoute(t *testing.T) {
	route, err := MapRoute(model.SessionRoute{
		Session:   model.Session{State: model.StateActive},
		SandboxID: "sbx-0123456789abcdef0123456789abcdef",
		Network:   model.NetworkAssignment{GuestIP: "172.16.0.2"},
	}, 3000)
	if err != nil {
		t.Fatal(err)
	}
	if route.Session.State != session.StateActive {
		t.Fatalf("state = %q", route.Session.State)
	}
	if route.SandboxID != "sbx-0123456789abcdef0123456789abcdef" {
		t.Fatalf("Sandbox ID = %q", route.SandboxID)
	}
	if route.Endpoint.Scheme != "http" || route.Endpoint.Authority != "172.16.0.2:3000" {
		t.Fatalf("endpoint = %+v", route.Endpoint)
	}
}

func TestMapRouteRejectsMissingSandboxIdentity(t *testing.T) {
	if _, err := MapRoute(model.SessionRoute{
		Session: model.Session{State: model.StateActive},
		Network: model.NetworkAssignment{GuestIP: "172.16.0.2"},
	}, 3000); err == nil {
		t.Fatal("route without a Sandbox ID was accepted")
	}
}

func TestMapEndpointRejectsInvalidFirecrackerRoutes(t *testing.T) {
	for _, test := range []struct {
		name    string
		network model.NetworkAssignment
		port    int
	}{
		{name: "missing IP", port: 3000},
		{name: "public IP", network: model.NetworkAssignment{GuestIP: "192.0.2.1"}, port: 3000},
		{name: "IPv6", network: model.NetworkAssignment{GuestIP: "fd00::2"}, port: 3000},
		{name: "zero port", network: model.NetworkAssignment{GuestIP: "172.16.0.2"}},
		{name: "large port", network: model.NetworkAssignment{GuestIP: "172.16.0.2"}, port: 65536},
	} {
		t.Run(test.name, func(t *testing.T) {
			if endpoint, err := MapEndpoint(test.network, test.port); err == nil {
				t.Fatalf("endpoint = %+v", endpoint)
			}
		})
	}
}
