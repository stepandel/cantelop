package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stepandel/cantelop/fire-fuse/controlauth"
	"github.com/stepandel/cantelop/fire-fuse/controlclient"
	"github.com/stepandel/cantelop/fire-fuse/host/observer"
)

func TestControlCredentialsEnforceServiceCapabilities(t *testing.T) {
	tokens := map[string]string{"operator": strings.Repeat("a", 64), "gateway": strings.Repeat("b", 64), "dispatcher": strings.Repeat("c", 64), "observer": strings.Repeat("d", 64)}
	credentials, err := controlCredentials(tokens["operator"], tokens["gateway"], tokens["dispatcher"], tokens["observer"])
	if err != nil {
		t.Fatal(err)
	}
	handler, err := controlauth.RequireScoped(credentials, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, role, method, path string
		status                   int
	}{
		{"operator_any", "operator", http.MethodPost, "/v1/sandboxes", http.StatusNoContent},
		{"gateway_session", "gateway", http.MethodPost, "/v1/sessions", http.StatusNoContent},
		{"gateway_sync_read", "gateway", http.MethodGet, "/v1/apps/app_a/workspace-sync/wsp_a", http.StatusNoContent},
		{"gateway_sync_write", "gateway", http.MethodPut, "/v1/apps/app_a/workspace-sync/wsp_a", http.StatusNoContent},
		{"gateway_sync_delete", "gateway", http.MethodDelete, "/v1/apps/app_a/workspace-sync/wsp_a", http.StatusNoContent},
		{"dispatcher_no_sync", "dispatcher", http.MethodGet, "/v1/apps/app_a/workspace-sync/wsp_a", http.StatusForbidden},
		{"observer_no_sync", "observer", http.MethodPut, "/v1/apps/app_a/workspace-sync/wsp_a", http.StatusForbidden},
		{"gateway_cannot_crash", "gateway", http.MethodPost, "/v1/sandboxes/sbx_a/crash", http.StatusForbidden},
		{"gateway_cannot_append_output", "gateway", http.MethodPost, "/v1/session-events", http.StatusForbidden},
		{"dispatcher_output", "dispatcher", http.MethodPost, "/v1/session-events", http.StatusNoContent},
		{"dispatcher_directory", "dispatcher", http.MethodGet, "/v1/apps", http.StatusNoContent},
		{"observer_snapshot", "observer", http.MethodGet, "/v1/workspaces?app_id=app_a", http.StatusNoContent},
		{"observer_app_builds", "observer", http.MethodGet, "/v1/apps/app_a", http.StatusNoContent},
		{"observer_mutation", "observer", http.MethodPost, "/v1/sandboxes/sbx_a/crash", http.StatusNoContent},
		{"observer_cannot_create", "observer", http.MethodPost, "/v1/sessions", http.StatusForbidden},
		{"observer_cannot_dispatch", "observer", http.MethodPost, "/v1/message-deliveries/claim", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(tc.method, tc.path, nil)
			request.Header.Set("Authorization", "Bearer "+tokens[tc.role])
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("status = %d", response.Code)
			}
		})
	}
}

func TestControlCredentialsMustBeDistinct(t *testing.T) {
	token := strings.Repeat("a", 64)
	credentials, err := controlCredentials(token, token, strings.Repeat("b", 64), strings.Repeat("c", 64))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := controlauth.RequireScoped(credentials, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})); err == nil {
		t.Fatal("shared service credential accepted")
	}
}

func TestControlTokenSetRejectsCrossPortCredentialCollision(t *testing.T) {
	tokens := map[string]string{"operator": strings.Repeat("a", 64), "gateway": strings.Repeat("b", 64), "dispatcher": strings.Repeat("c", 64), "observer": strings.Repeat("d", 64), "compute": strings.Repeat("e", 64)}
	if err := validateControlTokenSet(tokens); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"gateway", "dispatcher", "observer"} {
		colliding := make(map[string]string, len(tokens))
		for name, token := range tokens {
			colliding[name] = token
		}
		colliding["compute"] = colliding[role]
		if err := validateControlTokenSet(colliding); err == nil {
			t.Fatalf("compute and %s credential collision accepted", role)
		}
	}
}

func TestControlCredentialRouteMatrix(t *testing.T) {
	tokens := map[string]string{"operator": strings.Repeat("a", 64), "gateway": strings.Repeat("b", 64), "dispatcher": strings.Repeat("c", 64), "observer": strings.Repeat("d", 64)}
	credentials, err := controlCredentials(tokens["operator"], tokens["gateway"], tokens["dispatcher"], tokens["observer"])
	if err != nil {
		t.Fatal(err)
	}
	handler, err := controlauth.RequireScoped(credentials, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	if err != nil {
		t.Fatal(err)
	}
	type route struct {
		method, path string
		roles        []string
	}
	routes := []route{
		{http.MethodPost, "/v1/apps", nil},
		{http.MethodPost, "/v1/workspaces", nil},
		{http.MethodGet, "/v1/apps", []string{"gateway", "dispatcher", "observer"}},
		{http.MethodGet, "/v1/apps/app_a/workspaces/primary", nil},
		{http.MethodGet, "/v1/apps/app_a/workspaces/by-slug/main", []string{"gateway"}},
		{http.MethodGet, "/v1/workspaces?app_id=app_a", []string{"gateway", "dispatcher", "observer"}},
		{http.MethodGet, "/v1/workspaces/wsp_a", []string{"gateway", "dispatcher"}},
		{http.MethodPost, "/v1/sessions", []string{"gateway", "dispatcher"}},
		{http.MethodGet, "/v1/sessions?workspace_id=wsp_a", []string{"observer"}},
		{http.MethodGet, "/v1/sessions/ses_a", []string{"gateway", "dispatcher"}},
		{http.MethodGet, "/v1/sessions/ses_a/route?sandbox_id=sbx-a", []string{"gateway", "dispatcher"}},
		{http.MethodDelete, "/v1/sessions/ses_a", []string{"gateway", "observer"}},
		{http.MethodPost, "/v1/pinned-work-leases", []string{"dispatcher"}},
		{http.MethodGet, "/v1/message-executions", []string{"dispatcher"}},
		{http.MethodPost, "/v1/message-executions", []string{"dispatcher"}},
		{http.MethodPost, "/v1/message-deliveries", []string{"gateway"}},
		{http.MethodGet, "/v1/message-delivery-notifications?after=0", []string{"dispatcher"}},
		{http.MethodPost, "/v1/message-deliveries/claim", []string{"dispatcher"}},
		{http.MethodPost, "/v1/message-deliveries/requeue-handling", []string{"dispatcher"}},
		{http.MethodGet, "/v1/message-deliveries/msg_a", []string{"gateway", "dispatcher"}},
		{http.MethodGet, "/v1/message-deliveries/msg_a/wait", []string{"gateway"}},
		{http.MethodPost, "/v1/message-deliveries/msg_a/requeue", []string{"dispatcher"}},
		{http.MethodPost, "/v1/message-deliveries/msg_a/reply", []string{"dispatcher"}},
		{http.MethodPost, "/v1/message-deliveries/msg_a/fail", []string{"dispatcher"}},
		{http.MethodGet, "/v1/message-delivery-events?limit=1", []string{"observer"}},
		{http.MethodGet, "/v1/sandboxes", []string{"observer"}},
		{http.MethodGet, "/v1/sandboxes/sbx-a", []string{"observer"}},
		{http.MethodDelete, "/v1/sandboxes/sbx-a", []string{"dispatcher", "observer"}},
		{http.MethodPost, "/v1/sandboxes/sbx-a/crash", []string{"observer"}},
		{http.MethodGet, "/v1/sandbox-usage", []string{"observer"}},
		{http.MethodGet, "/v1/host-capacity", []string{"observer"}},
		{http.MethodGet, "/v1/request-events?limit=1", []string{"observer"}},
		{http.MethodPost, "/v1/session-events", []string{"dispatcher"}},
		{http.MethodGet, "/v1/session-events?workspace_id=w&session_id=s&after=0", []string{"gateway"}},
		{http.MethodGet, "/v1/session-view?workspace_id=w&session_id=s", []string{"gateway"}},
	}
	for _, candidate := range routes {
		for _, role := range []string{"operator", "gateway", "dispatcher", "observer"} {
			allowed := role == "operator"
			for _, expected := range candidate.roles {
				allowed = allowed || role == expected
			}
			request := httptest.NewRequest(candidate.method, candidate.path, nil)
			request.Header.Set("Authorization", "Bearer "+tokens[role])
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			want := http.StatusForbidden
			if allowed {
				want = http.StatusNoContent
			}
			if response.Code != want {
				t.Errorf("%s %s as %s = %d, want %d", candidate.method, candidate.path, role, response.Code, want)
			}
		}
	}
}

func TestObserverCredentialCanVerifyOneSandboxAssignment(t *testing.T) {
	token := strings.Repeat("d", 64)
	credentials, err := controlCredentials(strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64), token)
	if err != nil {
		t.Fatal(err)
	}
	id := "sbx-0123456789abcdef0123456789abcdef"
	calls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/sandboxes/{id}", func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.PathValue("id") != id {
			t.Fatal("wrong sandbox selector")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"` + id + `","app_id":"app_one","workspace_id":"wsp_one","state":"running"}`))
	})
	handler, err := controlauth.RequireScoped(credentials, mux)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	authenticated, err := controlauth.AuthenticateClient(server.Client(), token)
	if err != nil {
		t.Fatal(err)
	}
	client, err := observer.NewControlClient(server.URL, authenticated)
	if err != nil {
		t.Fatal(err)
	}
	assignment, status, err := client.DatabaseAccess(context.Background(), id)
	if err != nil || status != http.StatusOK || !assignment.Active || assignment.SandboxID != id || calls != 1 {
		t.Fatalf("scoped assignment = %#v, status = %d, calls = %d, error = %v", assignment, status, calls, err)
	}
}

func TestDispatcherCredentialSupportsWorkspaceReconciliation(t *testing.T) {
	dispatcherToken := strings.Repeat("c", 64)
	credentials, err := controlCredentials(strings.Repeat("a", 64), strings.Repeat("b", 64), dispatcherToken, strings.Repeat("d", 64))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/apps", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"id":"app_a"},{"id":"app_b"}]}`))
	})
	mux.HandleFunc("GET /v1/workspaces", func(w http.ResponseWriter, r *http.Request) {
		app := r.URL.Query().Get("app_id")
		if app != "app_a" && app != "app_b" {
			t.Errorf("unexpected directory scope: %q", app)
			http.Error(w, "invalid App", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"id":"wsp_` + app + `","app_id":"` + app + `"}]}`))
	})
	handler, err := controlauth.RequireScoped(credentials, mux)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	authenticated, err := controlauth.AuthenticateClient(server.Client(), dispatcherToken)
	if err != nil {
		t.Fatal(err)
	}
	client, err := controlclient.New(server.URL, authenticated)
	if err != nil {
		t.Fatal(err)
	}
	workspaces, err := client.ListWorkspaces(context.Background())
	if err != nil {
		t.Fatalf("dispatcher reconciliation directory: %v", err)
	}
	if len(workspaces) != 2 || workspaces[0].AppID != "app_a" || workspaces[1].AppID != "app_b" {
		t.Fatalf("reconciliation directory = %#v", workspaces)
	}
}

func TestGatewayCredentialCanInspectRouteBeforeMailboxStop(t *testing.T) {
	gatewayToken := strings.Repeat("b", 64)
	credentials, err := controlCredentials(strings.Repeat("a", 64), gatewayToken, strings.Repeat("c", 64), strings.Repeat("d", 64))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/sessions/ses_agent_test/route", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sandbox_id":"sbx-0123456789abcdef0123456789abcdef","endpoint":{"scheme":"http","authority":"172.16.0.2:3000"}}`))
	})
	handler, err := controlauth.RequireScoped(credentials, mux)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	authenticated, err := controlauth.AuthenticateClient(server.Client(), gatewayToken)
	if err != nil {
		t.Fatal(err)
	}
	client, err := controlclient.New(server.URL, authenticated)
	if err != nil {
		t.Fatal(err)
	}
	route, err := client.GetSessionRoute(context.Background(), "ses_agent_test")
	if err != nil {
		t.Fatalf("gateway pre-stop route lookup: %v", err)
	}
	if route.SandboxID != "sbx-0123456789abcdef0123456789abcdef" {
		t.Fatalf("route = %#v", route)
	}
}
