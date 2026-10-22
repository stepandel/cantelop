package observer

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/model"
)

type runtimePoolTransport struct {
	workspace Workspace
	session   Session
	sandbox   Sandbox
	ready     bool
	failPath  string
	mu        sync.Mutex
	requests  []string
}

func newRuntimePool(name, sandboxHash string) *runtimePoolTransport {
	return &runtimePoolTransport{
		workspace: Workspace{ID: "wsp_" + name, AppID: "app_" + name},
		session:   Session{ID: "ses_" + name, WorkspaceID: "wsp_" + name, State: SessionStateActive},
		sandbox:   Sandbox{ID: "sbx-" + strings.Repeat(sandboxHash, 32), WorkspaceID: "wsp_" + name, AppID: "app_" + name, State: "running"},
		ready:     true,
	}
}

func (p *runtimePoolTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	p.mu.Lock()
	p.requests = append(p.requests, r.Method+" "+r.URL.RequestURI())
	p.mu.Unlock()
	status := http.StatusOK
	var body any = struct{}{}
	switch r.URL.Path {
	case "/readyz":
		if !p.ready {
			status = http.StatusServiceUnavailable
		}
	case "/v1/host-capacity":
		body = HostCapacity{WarmSlots: WarmSlotCapacity{Target: 4, Ready: 2, Claimed: 1, Preparing: 1}}
	case "/v1/apps":
		apps := []map[string]string{}
		if p.workspace.ID != "" {
			apps = append(apps, map[string]string{"id": p.workspace.AppID})
		}
		body = map[string]any{"items": apps}
	case "/v1/workspaces":
		body = map[string]any{"items": []Workspace{p.workspace}}
	case "/v1/sessions":
		body = map[string]any{"items": []Session{p.session}}
	case "/v1/sandboxes":
		sandboxes := []Sandbox{}
		if p.workspace.ID != "" {
			sandboxes = append(sandboxes, p.sandbox)
		}
		body = map[string]any{"items": sandboxes}
	case "/v1/sandbox-usage":
		body = map[string]any{"observed_at": "2026-10-22T18:00:00Z", "items": []map[string]any{{
			"sandbox_id": p.sandbox.ID, "cpu_usage_us": 17, "guest_memory_used_bytes": 12345,
		}}}
	case "/v1/request-events":
		events := []RequestEvent{}
		query := r.URL.Query()
		if p.workspace.ID != "" && (query.Get("workspace_id") == p.workspace.ID || query.Get("app_id") == p.workspace.AppID ||
			(!query.Has("workspace_id") && !query.Has("app_id"))) {
			events = append(events, RequestEvent{Sequence: 1, EventID: "evt_" + p.workspace.ID, WorkspaceID: p.workspace.ID})
		}
		body = RequestEventPage{Items: events}
	case "/v1/apps/" + p.workspace.AppID:
		body = map[string]any{"builds": []AppBuild{{Version: 1, ID: "bld_" + p.workspace.AppID}}}
	case "/v1/sessions/" + p.session.ID:
		if r.Method != http.MethodDelete {
			status = http.StatusForbidden // Observer cannot GET individual Sessions.
		}
		body = p.session
	case "/v1/sandboxes/" + p.sandbox.ID, "/v1/sandboxes/" + p.sandbox.ID + "/crash":
		body = p.sandbox
	default:
		status = http.StatusNotFound
	}
	if r.URL.Path == p.failPath {
		status = http.StatusServiceUnavailable
	}
	document, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(document)))}, nil
}

func (p *runtimePoolTransport) count(prefix string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	count := 0
	for _, request := range p.requests {
		if strings.HasPrefix(request, prefix) {
			count++
		}
	}
	return count
}

func completeRuntimePools(t *testing.T, standard, managed *runtimePoolTransport) *CompleteSource {
	t.Helper()
	client := func(transport *runtimePoolTransport) *ControlClient {
		c, err := NewControlClient("http://127.0.0.1:8080", &http.Client{Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	var managedClient *ControlClient
	if managed != nil {
		managedClient = client(managed)
	}
	source, err := NewCompleteSourceWithManagedRuntime(client(standard), managedClient, &infrastructureSource{})
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func TestCompleteSourceIncludesBothRuntimePools(t *testing.T) {
	standard, managed := newRuntimePool("standard", "a"), newRuntimePool("managed", "b")
	source := completeRuntimePools(t, standard, managed)
	snapshot, err := source.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Workspaces) != 2 || len(snapshot.Sessions) != 2 || len(snapshot.Sandboxes) != 2 || !snapshot.Ready {
		t.Fatalf("combined snapshot = %#v", snapshot)
	}
	if snapshot.Workspaces[1].ID != managed.workspace.ID || snapshot.Sessions[1].AppID != managed.workspace.AppID ||
		snapshot.Sandboxes[1].Usage == nil || snapshot.Sandboxes[1].Usage.GuestMemoryUsedBytes == nil || *snapshot.Sandboxes[1].Usage.GuestMemoryUsedBytes != 12345 {
		t.Fatalf("managed records missing ownership or usage: %#v", snapshot)
	}
	if snapshot.Capacity.WarmSlots != (WarmSlotCapacity{Target: 8, Ready: 4, Claimed: 2, Preparing: 2}) {
		t.Fatalf("combined capacity = %#v", snapshot.Capacity)
	}
}

func TestCompleteSourceScopedHandlerFindsManagedWorkspaceWhenStandardPoolIsEmpty(t *testing.T) {
	standard, managed := newRuntimePool("standard", "a"), newRuntimePool("managed", "b")
	standard.workspace = Workspace{}
	source := completeRuntimePools(t, standard, managed)
	handler, err := NewHandlerWithManagedDatabaseAccess(source, source.ManagedDatabaseAccess(), strings.Repeat("r", 32), strings.Repeat("m", 32))
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/snapshot?workspace_ids="+managed.workspace.ID, nil)
	request.Header.Set("Authorization", "Bearer "+strings.Repeat("r", 32))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var snapshot Snapshot
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &snapshot) != nil {
		t.Fatalf("response = %d %s", response.Code, response.Body)
	}
	if len(snapshot.Sessions) != 1 || snapshot.Sessions[0].ID != managed.session.ID ||
		len(snapshot.Sandboxes) != 1 || snapshot.Sandboxes[0].ID != managed.sandbox.ID {
		t.Fatalf("managed workspace snapshot = %#v", snapshot)
	}
}

func TestCompleteSourceScopesBothPoolsAndPreservesEmptyScope(t *testing.T) {
	for _, ids := range [][]string{{"wsp_managed"}, {}} {
		standard, managed := newRuntimePool("standard", "a"), newRuntimePool("managed", "b")
		source := completeRuntimePools(t, standard, managed)
		snapshot, err := source.WorkspaceSnapshot(context.Background(), ids)
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshot.Workspaces) != len(ids) || len(snapshot.Sessions) != len(ids) || len(snapshot.Sandboxes) != len(ids) {
			t.Fatalf("scope %v = %#v", ids, snapshot)
		}
		if standard.count("GET /v1/sessions") != 0 || managed.count("GET /v1/sessions") != len(ids) {
			t.Fatal("Session reads escaped the Workspace scope")
		}
		if len(ids) == 0 && (standard.count("GET /v1/apps") != 0 || managed.count("GET /v1/apps") != 0 ||
			snapshot.Workspaces == nil || snapshot.Sessions == nil || snapshot.Sandboxes == nil) {
			t.Fatalf("empty scope read an inventory or returned null: %#v", snapshot)
		}
	}
}

func TestCompleteSourceDoesNotHideUnavailablePool(t *testing.T) {
	for _, failedPool := range []int{0, 1} {
		standard, managed := newRuntimePool("standard", "a"), newRuntimePool("managed", "b")
		[]*runtimePoolTransport{standard, managed}[failedPool].failPath = "/v1/sandboxes"
		if _, err := completeRuntimePools(t, standard, managed).Snapshot(context.Background()); err == nil {
			t.Fatal("unavailable pool appeared as a successful partial inventory")
		}
	}
	standard, managed := newRuntimePool("standard", "a"), newRuntimePool("managed", "b")
	managed.ready = false
	snapshot, err := completeRuntimePools(t, standard, managed).Snapshot(context.Background())
	if err != nil || snapshot.Ready || len(snapshot.Sessions) != 2 {
		t.Fatalf("not-ready pool lost its inventory: %#v, %v", snapshot, err)
	}
}

func TestCompleteSourceRejectsDuplicatePoolIdentities(t *testing.T) {
	for _, kind := range []string{"workspace", "session", "sandbox"} {
		t.Run(kind, func(t *testing.T) {
			standard, managed := newRuntimePool("standard", "a"), newRuntimePool("managed", "b")
			switch kind {
			case "workspace":
				managed.workspace.ID = standard.workspace.ID
			case "session":
				managed.session.ID = standard.session.ID
			case "sandbox":
				managed.sandbox.ID = standard.sandbox.ID
			}
			if _, err := completeRuntimePools(t, standard, managed).Snapshot(context.Background()); err == nil {
				t.Fatal("duplicate identity accepted")
			}
		})
	}
}

func TestCompleteSourceMutatesOnlyOwningPool(t *testing.T) {
	for _, owner := range []int{0, 1} {
		for _, action := range []string{"session-stop", "sandbox-stop", "sandbox-crash"} {
			standard, managed := newRuntimePool("standard", "a"), newRuntimePool("managed", "b")
			pools := []*runtimePoolTransport{standard, managed}
			source := completeRuntimePools(t, standard, managed)
			var status int
			var err error
			switch action {
			case "session-stop":
				_, status, err = source.TerminateSession(context.Background(), pools[owner].session.ID)
			case "sandbox-stop":
				_, status, err = source.TerminateSandbox(context.Background(), pools[owner].sandbox.ID)
			case "sandbox-crash":
				_, status, err = source.CrashSandbox(context.Background(), pools[owner].sandbox.ID)
			}
			if status != http.StatusOK || err != nil {
				t.Fatalf("%s pool %d: %d %v", action, owner, status, err)
			}
			for _, pool := range pools {
				if pool.count("GET /readyz")+pool.count("GET /v1/host-capacity")+pool.count("GET /v1/sandbox-usage") != 0 ||
					(action != "session-stop" && pool.count("GET /v1/apps") != 0) {
					t.Fatalf("%s owner lookup read the full inventory: %v", action, pool.requests)
				}
			}
			for i, pool := range pools {
				want := 0
				if i == owner {
					want = 1
				}
				if got := pool.count("DELETE ") + pool.count("POST "); got != want {
					t.Fatalf("%s pool %d mutations = %d, want %d", action, i, got, want)
				}
			}
		}
	}
}

func TestCompleteSourceNeverMutatesOnMissingAmbiguousOrUnavailableOwner(t *testing.T) {
	for _, scenario := range []string{"missing", "ambiguous", "unavailable"} {
		standard, managed := newRuntimePool("standard", "a"), newRuntimePool("managed", "b")
		id := "ses_missing"
		wantStatus := http.StatusNotFound
		switch scenario {
		case "ambiguous":
			managed.session.ID = standard.session.ID
			id = standard.session.ID
			wantStatus = http.StatusConflict
		case "unavailable":
			managed.failPath = "/v1/apps"
			id = managed.session.ID
			wantStatus = 0
		}
		_, status, err := completeRuntimePools(t, standard, managed).TerminateSession(context.Background(), id)
		if err == nil || status != wantStatus || standard.count("DELETE ")+managed.count("DELETE ") != 0 {
			t.Fatalf("%s owner: %d %v", scenario, status, err)
		}
	}
}

func TestCompleteSourceMutatesOwnerWhenOtherPoolIsUnavailable(t *testing.T) {
	for _, action := range []string{"session-stop", "sandbox-stop", "sandbox-crash"} {
		standard, managed := newRuntimePool("standard", "a"), newRuntimePool("managed", "b")
		source := completeRuntimePools(t, standard, managed)
		var status int
		var err error
		switch action {
		case "session-stop":
			managed.failPath = "/v1/apps"
			_, status, err = source.TerminateSession(context.Background(), standard.session.ID)
		case "sandbox-stop":
			managed.failPath = "/v1/sandboxes/" + standard.sandbox.ID
			_, status, err = source.TerminateSandbox(context.Background(), standard.sandbox.ID)
		case "sandbox-crash":
			managed.failPath = "/v1/sandboxes/" + standard.sandbox.ID
			_, status, err = source.CrashSandbox(context.Background(), standard.sandbox.ID)
		}
		if status != http.StatusOK || err != nil || standard.count("DELETE ")+standard.count("POST ") != 1 ||
			managed.count("DELETE ")+managed.count("POST ") != 0 {
			t.Fatalf("%s blocked by unavailable pool: %d %v", action, status, err)
		}
	}
}

func TestCompleteSourceNeverMutatesMissingOrAmbiguousSandbox(t *testing.T) {
	for _, scenario := range []string{"missing", "ambiguous", "unavailable"} {
		standard, managed := newRuntimePool("standard", "a"), newRuntimePool("managed", "b")
		id := "sbx-" + strings.Repeat("c", 32)
		wantStatus := http.StatusNotFound
		switch scenario {
		case "ambiguous":
			managed.sandbox.ID = standard.sandbox.ID
			id = standard.sandbox.ID
			wantStatus = http.StatusConflict
		case "unavailable":
			managed.failPath = "/v1/sandboxes/" + managed.sandbox.ID
			standard.failPath = "/v1/sandboxes/" + managed.sandbox.ID
			id = managed.sandbox.ID
			wantStatus = http.StatusServiceUnavailable
		}
		_, status, err := completeRuntimePools(t, standard, managed).TerminateSandbox(context.Background(), id)
		if err == nil || status != wantStatus || standard.count("DELETE ")+managed.count("DELETE ") != 0 {
			t.Fatalf("%s Sandbox owner: %d %v", scenario, status, err)
		}
	}
}

func TestCompleteSourceReadsHistoryAndBuildsFromOwningPool(t *testing.T) {
	standard, managed := newRuntimePool("standard", "a"), newRuntimePool("managed", "b")
	source := completeRuntimePools(t, standard, managed)
	for _, pool := range []*runtimePoolTransport{standard, managed} {
		for _, query := range []model.RequestEventQuery{{WorkspaceID: pool.workspace.ID, Limit: 10}, {AppID: pool.workspace.AppID, Limit: 10}} {
			page, err := source.RequestEvents(context.Background(), query)
			if err != nil || len(page.Items) != 1 || page.Items[0].WorkspaceID != pool.workspace.ID {
				t.Fatalf("history %#v = %#v, %v", query, page, err)
			}
		}
		builds, status, err := source.AppBuilds(context.Background(), pool.workspace.AppID)
		if err != nil || status != http.StatusOK || len(builds) != 1 || builds[0].ID != "bld_"+pool.workspace.AppID {
			t.Fatalf("builds for %s = %#v, %d, %v", pool.workspace.AppID, builds, status, err)
		}
	}
	page, err := source.RequestEvents(context.Background(), model.RequestEventQuery{WorkspaceID: "wsp_missing", Limit: 10})
	if err != nil || page.Items == nil || len(page.Items) != 0 {
		t.Fatalf("missing Workspace history = %#v, %v", page, err)
	}
	if _, err := source.RequestEvents(context.Background(), model.RequestEventQuery{Limit: 10}); err == nil {
		t.Fatal("unscoped history mixed runtime pools")
	}
	if _, status, err := source.AppBuilds(context.Background(), "app_missing"); err == nil || status != http.StatusNotFound {
		t.Fatalf("missing App builds = %d, %v", status, err)
	}
	managed.failPath = "/v1/request-events"
	if _, err := source.RequestEvents(context.Background(), model.RequestEventQuery{WorkspaceID: managed.workspace.ID, Limit: 10}); err == nil {
		t.Fatal("unavailable owning pool appeared as empty history")
	}
}

func TestCompleteSourceWithoutManagedRuntimePreservesSnapshot(t *testing.T) {
	standard := newRuntimePool("standard", "a")
	source := completeRuntimePools(t, standard, nil)
	source.ControlClient.now = func() time.Time { return time.Unix(123, 0) }
	want, err := source.ControlClient.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got, err := source.Snapshot(context.Background())
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("single runtime changed: %#v, %v", got, err)
	}
}
