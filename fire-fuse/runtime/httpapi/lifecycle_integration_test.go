package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/httpapi"
	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
	firecrackerruntime "github.com/stepandel/cantelop/fire-fuse/runtime/runtimeprovider/firecracker"
)

type fakeRuntime struct {
	mu      sync.Mutex
	starts  []microvm.Sandbox
	stops   []microvm.Sandbox
	running map[string]bool
}

func (r *fakeRuntime) Validate(microvm.RuntimeSpec) error { return nil }
func (r *fakeRuntime) Start(
	_ context.Context, sandbox microvm.Sandbox, _ microvm.RuntimeCheckpoint,
) (microvm.RuntimeInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.starts = append(r.starts, sandbox)
	r.running[sandbox.ID] = true
	network := microvm.NetworkAssignment{
		GuestIP: "172.16.0.2", GatewayIP: "172.16.0.1", Subnet: "172.16.0.0/30",
		GuestMAC: "02:00:00:00:00:02",
	}
	return microvm.RuntimeInfo{Network: network, Ownership: microvm.RuntimeOwnership{
		JailPrepared: true, AddressAllocated: true, TAPCreated: true,
		ForwardingApplied: true, TAPName: "fftest0", Network: network,
		Process: microvm.ProcessIdentity{
			PID: 123, BootID: "01234567-89ab-cdef-0123-456789abcdef", StartTimeTicks: 456,
		},
	}}, nil
}
func (r *fakeRuntime) Stop(
	_ context.Context, sandbox microvm.Sandbox, _ microvm.RuntimeCheckpoint,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stops = append(r.stops, sandbox)
	delete(r.running, sandbox.ID)
	return nil
}
func (r *fakeRuntime) IsRunning(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running[id]
}
func (r *fakeRuntime) RuntimeDone(string) (<-chan struct{}, bool) { return nil, false }
func (r *fakeRuntime) RuntimeExit(string) (microvm.RuntimeExit, bool) {
	return microvm.RuntimeExit{}, false
}

func (r *fakeRuntime) startCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.starts)
}

func (r *fakeRuntime) stopCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.stops)
}

type workspaceListBody struct {
	Items []struct {
		ID string `json:"id"`
	} `json:"items"`
}

type sessionBody struct {
	ID          string        `json:"id"`
	WorkspaceID string        `json:"workspace_id"`
	BuildID     string        `json:"build_id"`
	State       microvm.State `json:"state"`
}

func TestWorkspaceSessionHTTPIntegration(t *testing.T) {
	runtime := &fakeRuntime{running: make(map[string]bool)}
	handler := newLifecycleHandler(t, runtime)

	workspacesRecorder := serve(handler, http.MethodGet, "/v1/workspaces?app_id=app-test", "")
	if workspacesRecorder.Code != http.StatusOK {
		t.Fatalf("GET Workspaces = %d: %s", workspacesRecorder.Code, workspacesRecorder.Body.String())
	}
	workspaces := decodeJSON[workspaceListBody](t, workspacesRecorder)
	if len(workspaces.Items) != 1 {
		t.Fatalf("Workspaces = %#v", workspaces.Items)
	}
	if !strings.Contains(workspacesRecorder.Body.String(), `"app_slug":"test-app"`) || strings.Contains(workspacesRecorder.Body.String(), `"hostname"`) {
		t.Fatalf("Workspace routing fields = %s", workspacesRecorder.Body.String())
	}
	workspaceID := workspaces.Items[0].ID
	resolved := serve(handler, http.MethodGet, "/v1/apps/app-test/workspaces/by-slug/app-test", "")
	if resolved.Code != http.StatusOK || !strings.Contains(resolved.Body.String(), `"id":"`+workspaceID+`"`) {
		t.Fatalf("GET Workspace by slug = %d: %s", resolved.Code, resolved.Body.String())
	}
	if removed := serve(handler, http.MethodGet, "/v1/apps/app-test/workspaces/primary", ""); removed.Code == http.StatusOK {
		t.Fatalf("GET primary Workspace = %d: %s", removed.Code, removed.Body.String())
	}
	createdRecorder := serve(
		handler, http.MethodPost, "/v1/sessions",
		`{"id":"ses_integration","workspace_id":"`+workspaceID+`","keep_alive_seconds":300}`,
	)
	if createdRecorder.Code != http.StatusAccepted {
		t.Fatalf("POST Session = %d: %s", createdRecorder.Code, createdRecorder.Body.String())
	}
	created := decodeJSON[sessionBody](t, createdRecorder)
	if created.ID != "ses_integration" || created.WorkspaceID != workspaceID ||
		created.State != microvm.StateActive || created.BuildID == "" || runtime.startCount() != 1 {
		t.Fatalf("Session/runtime starts = %#v/%d", created, runtime.startCount())
	}
	for _, private := range []string{`"sandbox_id"`, `"profile"`, `"vcpus"`, `"memory_mib"`, `"network"`} {
		if strings.Contains(createdRecorder.Body.String(), private) {
			t.Errorf("response exposes %s: %s", private, createdRecorder.Body.String())
		}
	}
	routeRecorder := serve(handler, http.MethodGet, "/v1/sessions/"+created.ID+"/route", "")
	if routeRecorder.Code != http.StatusOK {
		t.Fatalf("GET Session route = %d: %s", routeRecorder.Code, routeRecorder.Body.String())
	}
	if !strings.Contains(routeRecorder.Body.String(), `"scheme":"http"`) ||
		!strings.Contains(routeRecorder.Body.String(), `"authority":"172.16.0.2:3000"`) ||
		strings.Contains(routeRecorder.Body.String(), `"guest_ip"`) {
		t.Fatalf("Session route = %s", routeRecorder.Body.String())
	}
	leaseID := "lease_0123456789abcdef0123456789abcdef"
	acquiredRecorder := serve(handler, http.MethodPost, "/v1/sessions/"+created.ID+"/work-leases",
		`{"lease_id":"`+leaseID+`","ttl_seconds":60}`)
	if acquiredRecorder.Code != http.StatusOK || !strings.Contains(acquiredRecorder.Body.String(), `"lease_id":"`+leaseID+`"`) {
		t.Fatalf("POST Session work lease = %d: %s", acquiredRecorder.Code, acquiredRecorder.Body.String())
	}
	renewedRecorder := serve(handler, http.MethodPost, "/v1/sessions/"+created.ID+"/work-leases/"+leaseID+"/renew",
		`{"ttl_seconds":120}`)
	if renewedRecorder.Code != http.StatusOK || !strings.Contains(renewedRecorder.Body.String(), `"lease_id":"`+leaseID+`"`) {
		t.Fatalf("POST Session work lease renewal = %d: %s", renewedRecorder.Code, renewedRecorder.Body.String())
	}
	releasedRecorder := serve(handler, http.MethodDelete, "/v1/sessions/"+created.ID+"/work-leases/"+leaseID, "")
	if releasedRecorder.Code != http.StatusOK {
		t.Fatalf("DELETE Session work lease = %d: %s", releasedRecorder.Code, releasedRecorder.Body.String())
	}
	terminatedRecorder := serve(handler, http.MethodDelete, "/v1/sessions/"+created.ID, "")
	terminated := decodeJSON[sessionBody](t, terminatedRecorder)
	if terminatedRecorder.Code != http.StatusOK || terminated.State != microvm.StateIdle || runtime.stopCount() != 1 {
		t.Fatalf("DELETE Session = %d %#v, stops %d", terminatedRecorder.Code, terminated, runtime.stopCount())
	}

}

func newLifecycleHandler(t *testing.T, runtime microvm.Supervisor) http.Handler {
	t.Helper()
	store, err := microvm.OpenSQLiteStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return newLifecycleHandlerWithStore(store, runtime)
}

func newLifecycleHandlerWithStore(store microvm.Store, runtime microvm.Supervisor) http.Handler {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	buildID := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := store.EnsureComputeTenant(microvm.ComputeTenant{ID: "usr_test", CreatedAt: now}); err != nil {
		panic(err)
	}
	app := microvm.App{ID: "app-test", Slug: "test-app", CreatedAt: now, UpdatedAt: now}
	sessionRuntime := microvm.AppSessionRuntime{ActiveBuildID: buildID, Runtime: microvm.AppRuntimeSpec{Profile: microvm.DefaultSandboxProfileName, InternalPort: 3000}}
	build := microvm.AppBuild{AppID: app.ID, BuildID: buildID, CreatedAt: now}
	workspace := microvm.Workspace{ID: "wsp_0123456789abcdef0123456789abcdef", AppID: app.ID, Slug: "app-test", CreatedAt: now, UpdatedAt: now}
	if err := store.CreateAppForComputeTenant("usr_test", app, sessionRuntime, build); err != nil {
		panic(err)
	}
	if err := store.CreateWorkspace(workspace); err != nil {
		panic(err)
	}
	if _, err := store.SetReleaseAdmissionForComputeTenant("usr_test", app.ID, "act_00000000000000000000000000000000", true); err != nil {
		panic(err)
	}
	if _, err := store.SetReleaseAdmissionForComputeTenant("usr_test", app.ID, "act_00000000000000000000000000000000", false); err != nil {
		panic(err)
	}
	service := microvm.NewServiceWithSupervisor(store, runtime)
	provider, err := firecrackerruntime.New(service)
	if err != nil {
		panic(err)
	}
	return httpapi.NewHandlerWithRuntimeProvider(service, provider)
}

func serve(handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(method, path, strings.NewReader(body)))
	return recorder
}

func decodeJSON[T any](t *testing.T, recorder *httptest.ResponseRecorder) T {
	t.Helper()
	var body T
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v; body = %s", err, recorder.Body.String())
	}
	return body
}
