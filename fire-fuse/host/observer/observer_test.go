package observer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/model"
)

type sourceFunc func(context.Context) (Snapshot, error)

func (f sourceFunc) Snapshot(ctx context.Context) (Snapshot, error) { return f(ctx) }

type historySource struct {
	snapshot Snapshot
	page     RequestEventPage
	query    model.RequestEventQuery
}

type mutableSource struct {
	historySource
	terminated string
	crashed    string
}

type infrastructureSource struct {
	historySource
	snapshot InfrastructureSnapshot
	window   InfrastructureRange
	err      error
}

func (s *infrastructureSource) Infrastructure(_ context.Context, window InfrastructureRange) (InfrastructureSnapshot, error) {
	s.window = window
	return s.snapshot, s.err
}

func (s *mutableSource) TerminateSession(_ context.Context, id string) (Session, int, error) {
	s.terminated = id
	return Session{ID: id, WorkspaceID: "wsp_one", State: SessionStateIdle}, http.StatusOK, nil
}

func (s *mutableSource) TerminateSandbox(_ context.Context, id string) (Sandbox, int, error) {
	s.terminated = id
	return Sandbox{ID: id, WorkspaceID: "wsp_one", State: SandboxState("terminated")}, http.StatusOK, nil
}

func (s *mutableSource) CrashSandbox(_ context.Context, id string) (Sandbox, int, error) {
	s.crashed = id
	return Sandbox{ID: id, WorkspaceID: "wsp_one", State: SandboxState("failed"), Failure: &Failure{Reason: "runtime_exit"}}, http.StatusOK, nil
}

func (s *historySource) Snapshot(context.Context) (Snapshot, error) { return s.snapshot, nil }

func (s *historySource) RequestEvents(_ context.Context, query model.RequestEventQuery) (RequestEventPage, error) {
	s.query = query
	return s.page, nil
}

func TestHandlerRequiresTokenAndReturnsSnapshot(t *testing.T) {
	token := strings.Repeat("a", 32)
	startupCompletedAt := time.Date(2026, 8, 4, 0, 0, 1, 250000000, time.UTC)
	want := Snapshot{Ready: true, ObservedAt: time.Unix(1, 0).UTC(), Sessions: []Session{{
		ID: "ses_one", WorkspaceID: "wsp_one", State: SessionStateActive,
		Startup: &Startup{CompletedAt: startupCompletedAt, DurationMS: 1250},
	}}}
	handler, err := NewHandler(sourceFunc(func(context.Context) (Snapshot, error) { return want, nil }), token)
	if err != nil {
		t.Fatal(err)
	}

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/snapshot", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}

	request := httptest.NewRequest(http.MethodGet, "/snapshot", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("response = %d, headers %#v", response.Code, response.Header())
	}
	var got Snapshot
	if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got.Sessions) != 1 || got.Sessions[0].ID != "ses_one" ||
		got.Sessions[0].Startup == nil ||
		!got.Sessions[0].Startup.CompletedAt.Equal(startupCompletedAt) ||
		got.Sessions[0].Startup.DurationMS != 1250 {
		t.Fatalf("snapshot = %#v", got)
	}
}

func TestHandlerReturnsAuthenticatedBoundedRequestHistory(t *testing.T) {
	token := strings.Repeat("b", 32)
	source := &historySource{page: RequestEventPage{Items: []RequestEvent{{
		Sequence: 9, EventID: "request-9", WorkspaceID: "wsp_one", SessionID: "ses_one",
		ReceivedAt: time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC),
		StatusCode: 200, DurationMS: 12.5,
	}}, NextBeforeSequence: 9}}
	handler, err := NewHandler(source, token)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/requests?workspace_id=wsp_one&limit=25&before_sequence=10", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("response = %d, headers %#v", response.Code, response.Header())
	}
	var page RequestEventPage
	if err := json.NewDecoder(response.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Sequence != 9 || page.NextBeforeSequence != 9 ||
		source.query.WorkspaceID != "wsp_one" || source.query.Limit != 25 || source.query.BeforeSequence != 10 {
		t.Fatalf("page/query = %#v / %#v", page, source.query)
	}
	forward := httptest.NewRequest(http.MethodGet, "/requests?app_id=app_one&limit=500&after_sequence=8", nil)
	forward.Header.Set("Authorization", "Bearer "+token)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, forward)
	if response.Code != http.StatusOK || source.query.AppID != "app_one" || source.query.AfterSequence != 8 || source.query.BeforeSequence != 0 {
		t.Fatalf("forward = %d, query %#v", response.Code, source.query)
	}
	appRequest := httptest.NewRequest(http.MethodGet, "/requests?app_id=app_one&limit=25", nil)
	appRequest.Header.Set("Authorization", "Bearer "+token)
	appResponse := httptest.NewRecorder()
	handler.ServeHTTP(appResponse, appRequest)
	if appResponse.Code != http.StatusOK || source.query.AppID != "app_one" || source.query.WorkspaceID != "" {
		t.Fatalf("App request = %d / %#v", appResponse.Code, source.query)
	}

	invalid := httptest.NewRecorder()
	invalidRequest := httptest.NewRequest(http.MethodGet, "/requests?limit=501", nil)
	invalidRequest.Header.Set("Authorization", "Bearer "+token)
	handler.ServeHTTP(invalid, invalidRequest)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid query status = %d", invalid.Code)
	}
}

func TestHandlerReturnsAuthenticatedInfrastructureMetrics(t *testing.T) {
	token := strings.Repeat("i", 32)
	source := &infrastructureSource{snapshot: InfrastructureSnapshot{
		ObservedAt: time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC),
		Range:      "6h",
		Charts: []InfrastructureChart{{
			ID: "fire_fuse.health", Title: "Service health", Units: "status",
			Dimensions: []string{"fire-fuse"},
			Points: []InfrastructurePoint{{
				At: time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC), Values: []*float64{floatPointer(1)},
			}},
		}},
	}}
	handler, err := NewHandler(source, token)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/infrastructure?range=6h", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("response = %d, headers %#v: %s", response.Code, response.Header(), response.Body.String())
	}
	var got InfrastructureSnapshot
	if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Range != "6h" || len(got.Charts) != 1 || got.Charts[0].ID != "fire_fuse.health" || source.window.Name != "6h" {
		t.Fatalf("snapshot/window = %#v / %#v", got, source.window)
	}

	invalid := httptest.NewRecorder()
	invalidRequest := httptest.NewRequest(http.MethodGet, "/infrastructure?range=7d", nil)
	invalidRequest.Header.Set("Authorization", "Bearer "+token)
	handler.ServeHTTP(invalid, invalidRequest)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid query status = %d", invalid.Code)
	}
}

func TestHandlerSeparatesReadAndSessionMutationCredentials(t *testing.T) {
	readToken := strings.Repeat("r", 32)
	mutationToken := strings.Repeat("m", 32)
	source := &mutableSource{}
	handler, err := NewHandlerWithSessionMutations(source, readToken, mutationToken)
	if err != nil {
		t.Fatal(err)
	}

	denied := httptest.NewRecorder()
	deniedRequest := httptest.NewRequest(http.MethodDelete, "/sessions/ses_one", nil)
	deniedRequest.Header.Set("Authorization", "Bearer "+readToken)
	handler.ServeHTTP(denied, deniedRequest)
	if denied.Code != http.StatusUnauthorized || source.terminated != "" {
		t.Fatalf("read credential mutation = %d, %q", denied.Code, source.terminated)
	}

	request := httptest.NewRequest(http.MethodDelete, "/sessions/ses_one", nil)
	request.Header.Set("Authorization", "Bearer "+mutationToken)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || source.terminated != "ses_one" {
		t.Fatalf("mutation = %d, %q: %s", response.Code, source.terminated, response.Body.String())
	}
}

func TestHandlerSeparatesReadAndSandboxMutationCredentials(t *testing.T) {
	readToken := strings.Repeat("r", 32)
	mutationToken := strings.Repeat("m", 32)
	source := &mutableSource{}
	handler, err := NewHandlerWithSessionMutations(source, readToken, mutationToken)
	if err != nil {
		t.Fatal(err)
	}

	denied := httptest.NewRecorder()
	deniedRequest := httptest.NewRequest(http.MethodDelete, "/sandboxes/sbx-0123456789abcdef0123456789abcdef", nil)
	deniedRequest.Header.Set("Authorization", "Bearer "+readToken)
	handler.ServeHTTP(denied, deniedRequest)
	if denied.Code != http.StatusUnauthorized || source.terminated != "" {
		t.Fatalf("read credential mutation = %d, %q", denied.Code, source.terminated)
	}

	request := httptest.NewRequest(http.MethodDelete, "/sandboxes/sbx-0123456789abcdef0123456789abcdef", nil)
	request.Header.Set("Authorization", "Bearer "+mutationToken)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || source.terminated != "sbx-0123456789abcdef0123456789abcdef" {
		t.Fatalf("mutation = %d, %q: %s", response.Code, source.terminated, response.Body.String())
	}
}

func TestHandlerProtectsSandboxCrashInjectionWithMutationCredential(t *testing.T) {
	readToken := strings.Repeat("r", 32)
	mutationToken := strings.Repeat("m", 32)
	source := &mutableSource{}
	handler, err := NewHandlerWithSessionMutations(source, readToken, mutationToken)
	if err != nil {
		t.Fatal(err)
	}
	path := "/sandboxes/sbx-0123456789abcdef0123456789abcdef/crash"

	denied := httptest.NewRecorder()
	deniedRequest := httptest.NewRequest(http.MethodPost, path, nil)
	deniedRequest.Header.Set("Authorization", "Bearer "+readToken)
	handler.ServeHTTP(denied, deniedRequest)
	if denied.Code != http.StatusUnauthorized || source.crashed != "" {
		t.Fatalf("read credential crash = %d, %q", denied.Code, source.crashed)
	}

	request := httptest.NewRequest(http.MethodPost, path, nil)
	request.Header.Set("Authorization", "Bearer "+mutationToken)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || source.crashed != "sbx-0123456789abcdef0123456789abcdef" ||
		!strings.Contains(response.Body.String(), `"state":"failed"`) {
		t.Fatalf("crash = %d, %q: %s", response.Code, source.crashed, response.Body.String())
	}
}

func TestControlClientReadsRequestHistory(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		workspaceQuery := r.URL.Query().Get("workspace_id") == "wsp_one" && r.URL.Query().Get("before_sequence") == "20"
		appQuery := r.URL.Query().Get("app_id") == "app_one" && !r.URL.Query().Has("workspace_id") && r.URL.Query().Get("after_sequence") == "18"
		if r.URL.Path != "/v1/request-events" || (!workspaceQuery && !appQuery) || r.URL.Query().Get("limit") != "10" {
			t.Errorf("request URL = %s", r.URL.String())
		}
		body := `{"items":[{"sequence":19,"event_id":"request-19","workspace_id":"wsp_one",` +
			`"received_at":"2026-08-05T12:00:00Z","status_code":204,"duration_ms":4.5}]}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	client, err := NewControlClient("http://127.0.0.1:8080", &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	page, err := client.RequestEvents(context.Background(), model.RequestEventQuery{
		WorkspaceID: "wsp_one", BeforeSequence: 20, Limit: 10,
	})
	if err != nil || len(page.Items) != 1 || page.Items[0].Sequence != 19 || page.Items[0].StatusCode != 204 {
		t.Fatalf("RequestEvents() = %#v, %v", page, err)
	}
	_, err = client.RequestEvents(context.Background(), model.RequestEventQuery{AppID: "app_one", AfterSequence: 18, Limit: 10})
	if err != nil {
		t.Fatalf("App RequestEvents() = %v", err)
	}
}

func TestControlClientTerminatesSession(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodDelete || r.URL.Path != "/v1/sessions/ses_one" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		body := `{"id":"ses_one","workspace_id":"wsp_one","state":"idle"}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	client, err := NewControlClient("http://127.0.0.1:8080", &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	sessionRecord, status, err := client.TerminateSession(context.Background(), "ses_one")
	if err != nil || status != http.StatusOK || sessionRecord.ID != "ses_one" || sessionRecord.State != SessionStateIdle {
		t.Fatalf("TerminateSession() = %#v, %d, %v", sessionRecord, status, err)
	}
}

func TestControlClientTerminatesSandbox(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodDelete || r.URL.Path != "/v1/sandboxes/sbx-0123456789abcdef0123456789abcdef" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		body := `{"id":"sbx-0123456789abcdef0123456789abcdef","workspace_id":"wsp_one","state":"terminated"}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	client, err := NewControlClient("http://127.0.0.1:8080", &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	sessionRecord, status, err := client.TerminateSandbox(context.Background(), "sbx-0123456789abcdef0123456789abcdef")
	if err != nil || status != http.StatusOK || sessionRecord.ID != "sbx-0123456789abcdef0123456789abcdef" || sessionRecord.State != SandboxState("terminated") {
		t.Fatalf("TerminateSandbox() = %#v, %d, %v", sessionRecord, status, err)
	}
}

func TestControlClientCrashesSandbox(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/sandboxes/sbx-0123456789abcdef0123456789abcdef/crash" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		body := `{"id":"sbx-0123456789abcdef0123456789abcdef","workspace_id":"wsp_one","state":"failed","failure":{"reason":"runtime_exit"}}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	client, err := NewControlClient("http://127.0.0.1:8080", &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	record, status, err := client.CrashSandbox(context.Background(), "sbx-0123456789abcdef0123456789abcdef")
	if err != nil || status != http.StatusOK || record.State != SandboxState("failed") || record.Failure == nil || record.Failure.Reason != "runtime_exit" {
		t.Fatalf("CrashSandbox() = %#v, %d, %v", record, status, err)
	}
}

func TestControlClientReadsCurrentState(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, status := `{"status":"ready"}`, http.StatusOK
		switch r.URL.Path {
		case "/v1/host-capacity":
			body = `{"warm_slots":{"target":16,"ready":12,"claimed":3,"preparing":1}}`
		case "/v1/apps":
			body = `{"items":[{"id":"app-1"}]}`
		case "/v1/workspaces":
			body = `{"items":[{"id":"wsp_one","app_id":"app-1","slug":"production"}]}`
		case "/v1/sessions":
			body = `{"items":[{"id":"ses_one","workspace_id":"wsp_one","build_id":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","keep_alive_seconds":600,"state":"running","startup":{"completed_at":"2026-08-04T00:00:01.25Z","duration_ms":1250},"created_at":"2026-08-04T00:00:00Z","updated_at":"2026-08-04T00:01:00Z"}]}`
		case "/v1/sandboxes":
			body = `{"items":[{"id":"sbx_one","workspace_id":"wsp_one","app_id":"app-1","profile":"default","build_id":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","vcpus":1,"memory_mib":1024,"state":"running","created_at":"2026-08-04T00:00:00Z","updated_at":"2026-08-04T00:01:00Z"}]}`
		case "/v1/sandbox-usage":
			body = `{"observed_at":"2026-08-04T00:01:01Z","items":[{"sandbox_id":"sbx_one","cpu_usage_us":42,"memory_current_bytes":1048576,"guest_memory_used_bytes":262144}]}`
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	client, err := NewControlClient("http://127.0.0.1:8080", &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	client.now = func() time.Time { return time.Unix(2, 0) }
	snapshot, err := client.Snapshot(context.Background())
	if err != nil || !snapshot.Ready || len(snapshot.Workspaces) != 1 || len(snapshot.Sessions) != 1 || len(snapshot.Sandboxes) != 1 ||
		snapshot.Capacity.WarmSlots.Ready != 12 {
		t.Fatalf("Snapshot() = %#v, %v", snapshot, err)
	}
	if snapshot.Sessions[0].WorkspaceID != "wsp_one" || snapshot.Workspaces[0].Slug != "production" {
		t.Fatalf("Snapshot model = %#v", snapshot)
	}
	if snapshot.Sessions[0].KeepAliveSeconds != 600 || snapshot.Sandboxes[0].ID != "sbx_one" ||
		snapshot.Sandboxes[0].Usage == nil || snapshot.Sandboxes[0].Usage.CPUUsageUS != 42 ||
		snapshot.Sandboxes[0].Usage.GuestMemoryUsedBytes == nil || *snapshot.Sandboxes[0].Usage.GuestMemoryUsedBytes != 262144 {
		t.Fatalf("Snapshot inventories = %#v / %#v", snapshot.Sessions, snapshot.Sandboxes)
	}
	startup := snapshot.Sessions[0].Startup
	if startup == nil || startup.DurationMS != 1250 || !startup.CompletedAt.Equal(time.Date(2026, 8, 4, 0, 0, 1, 250000000, time.UTC)) {
		t.Fatalf("Snapshot startup = %#v", startup)
	}
}

func TestControlClientReturnsEmptyWorkspaceInventory(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, status := `{"status":"ready"}`, http.StatusOK
		if r.URL.Path == "/v1/apps" {
			body = `{"items":[]}`
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	client, err := NewControlClient("http://127.0.0.1:8080", &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := client.Snapshot(context.Background())
	if err != nil || len(snapshot.Workspaces) != 0 || len(snapshot.Sessions) != 0 || len(snapshot.Sandboxes) != 0 {
		t.Fatalf("Snapshot() = %#v, %v", snapshot, err)
	}
}

func netdataResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func TestNetdataClientReadsEachHostThroughTheParent(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	requested := map[string][]string{}
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		query := r.URL.Query()
		mu.Lock()
		requested[r.URL.Path] = append(requested[r.URL.Path], query.Get("chart")+"|"+strings.Join(query["dimension"], ","))
		mu.Unlock()
		switch r.URL.Path {
		case "/api/v1/chart":
			return netdataResponse(http.StatusOK, fmt.Sprintf(`{"id":"system.cpu","last_entry":%d}`, now.Add(-5*time.Second).Unix())), nil
		case "/host/storage/api/v1/chart":
			// Streamed once, then went quiet ten minutes ago.
			return netdataResponse(http.StatusOK, fmt.Sprintf(`{"id":"system.cpu","last_entry":%d}`, now.Add(-10*time.Minute).Unix())), nil
		case "/host/builder/api/v1/chart":
			// Never connected to the parent.
			return netdataResponse(http.StatusNotFound, "not found"), nil
		case "/api/v1/data", "/host/storage/api/v1/data":
			if query.Get("after") != "-21600" || query.Get("before") != "0" || query.Get("format") != "json" ||
				query.Get("group") != "average" || query.Get("options") != "seconds" || query.Get("points") != "144" {
				t.Errorf("request URL = %s", r.URL.String())
			}
			if query.Get("chart") == "fire_fuse.redis_rejections" {
				return netdataResponse(http.StatusNotFound, "chart not found"), nil
			}
			return netdataResponse(http.StatusOK, `{"labels":["time","available"],"data":[[1788199200,1],[1788199230,null]]}`), nil
		}
		t.Errorf("unexpected Netdata path %s", r.URL.Path)
		return netdataResponse(http.StatusNotFound, ""), nil
	})
	client, err := NewNetdataClient("http://127.0.0.1:19999", &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	client.now = func() time.Time { return now }
	snapshot, err := client.Infrastructure(context.Background(), infrastructureRanges["6h"])
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Range != "6h" || len(snapshot.Hosts) != 3 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	compute, storage, builder := snapshot.Hosts[0], snapshot.Hosts[1], snapshot.Hosts[2]
	if compute.ID != "compute" || !compute.Reachable || len(compute.Charts) != len(infrastructureHostSpecs[0].Charts) {
		t.Fatalf("compute = %#v", compute)
	}
	if len(snapshot.Charts) != len(compute.Charts) || snapshot.Charts[0].ID != "fire_fuse.health" ||
		snapshot.Charts[0].Dimensions[0] != "available" || snapshot.Charts[0].Points[1].Values[0] != nil {
		t.Fatalf("legacy charts = %#v", snapshot.Charts)
	}
	if storage.ID != "storage" || storage.Reachable || storage.LastCollectedAt == nil ||
		len(storage.Charts) != len(infrastructureHostSpecs[1].Charts)-1 {
		t.Fatalf("storage = %#v", storage)
	}
	for _, chart := range storage.Charts {
		if chart.ID == "fire_fuse.redis_rejections" {
			t.Fatal("missing storage chart was reported")
		}
	}
	if builder.ID != "builder" || builder.Reachable || builder.LastCollectedAt != nil || len(builder.Charts) != 0 {
		t.Fatalf("builder = %#v", builder)
	}
	for i, spec := range infrastructureHostSpecs[0].Charts {
		want := spec.ID + "|" + strings.Join(spec.Dimensions, ",")
		found := false
		for _, got := range requested["/api/v1/data"] {
			found = found || got == want
		}
		if !found {
			t.Fatalf("compute chart %d %q was not requested: %v", i, want, requested["/api/v1/data"])
		}
	}
	if !strings.Contains(strings.Join(requested["/host/storage/api/v1/data"], ";"), "fire_fuse.health|redis,minio") {
		t.Fatalf("storage health dimensions were not filtered: %v", requested["/host/storage/api/v1/data"])
	}
	if got := strings.Join(infrastructureHostSpecs[2].Charts[0].Dimensions, ","); got != "control_worker,build_materializer" {
		t.Fatalf("builder health dimensions = %q", got)
	}
	if len(requested["/host/builder/api/v1/data"]) != 0 {
		t.Fatalf("charts were read for an unknown host: %v", requested)
	}
}

func TestNetdataClientRequiresTheComputeParent(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/api/v1/chart" {
			return netdataResponse(http.StatusServiceUnavailable, ""), nil
		}
		return netdataResponse(http.StatusNotFound, ""), nil
	})
	client, err := NewNetdataClient("http://127.0.0.1:19999", &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Infrastructure(context.Background(), infrastructureRanges["1h"]); err == nil ||
		!strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("parent failure error = %v", err)
	}
}

func TestNetdataClientRejectsNonLoopbackAndUnboundedResults(t *testing.T) {
	if _, err := NewNetdataClient("https://metrics.example.com", nil); err == nil {
		t.Fatal("NewNetdataClient(non-loopback) error = nil")
	}
	rows := make([]string, infrastructureRanges["1h"].Points+1)
	for i := range rows {
		rows[i] = fmt.Sprintf("[%d,1]", 1788199200+i)
	}
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/api/v1/chart") {
			return netdataResponse(http.StatusOK, `{"last_entry":1788199200}`), nil
		}
		return netdataResponse(http.StatusOK, `{"labels":["time","available"],"data":[`+strings.Join(rows, ",")+`]}`), nil
	})
	client, err := NewNetdataClient("http://127.0.0.1:19999", &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Infrastructure(context.Background(), infrastructureRanges["1h"]); err == nil ||
		!strings.Contains(err.Error(), "invalid series") {
		t.Fatalf("unbounded result error = %v", err)
	}
}

func TestControlClientAllowsBoundedLargeSandboxInventory(t *testing.T) {
	largeInventory := `{"items":[],"padding":"` + strings.Repeat("a", maxControlResponseBytes) + `"}`
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(largeInventory)),
			Header:     make(http.Header),
		}, nil
	})
	client, err := NewControlClient("http://127.0.0.1:8080", &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	var page struct {
		Items []Sandbox `json:"items"`
	}
	if _, err := client.get(context.Background(), client.baseURL, "/v1/sandboxes", &page); err != nil {
		t.Fatalf("large sandbox inventory: %v", err)
	}
}

func TestControlClientKeepsOtherResponsesBelowTwoMiB(t *testing.T) {
	largeResponse := `{"items":[],"padding":"` + strings.Repeat("a", maxControlResponseBytes) + `"}`
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(largeResponse)),
			Header:     make(http.Header),
		}, nil
	})
	client, err := NewControlClient("http://127.0.0.1:8080", &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	var page struct {
		Items []Workspace `json:"items"`
	}
	if _, err := client.get(context.Background(), client.baseURL, "/v1/apps", &page); err == nil ||
		!strings.Contains(err.Error(), "exceeds 2 MiB") {
		t.Fatalf("large non-sandbox response error = %v", err)
	}
}

func TestControlClientRejectsSandboxInventoryAboveEightMiB(t *testing.T) {
	largeInventory := `{"items":[],"padding":"` + strings.Repeat("a", maxSandboxInventoryResponseBytes) + `"}`
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(largeInventory)),
			Header:     make(http.Header),
		}, nil
	})
	client, err := NewControlClient("http://127.0.0.1:8080", &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	var page struct {
		Items []Sandbox `json:"items"`
	}
	if _, err := client.get(context.Background(), client.baseURL, "/v1/sandboxes", &page); err == nil ||
		!strings.Contains(err.Error(), "exceeds 8 MiB") {
		t.Fatalf("unbounded sandbox inventory error = %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestControlClientRequiresLoopbackOrigin(t *testing.T) {
	for _, endpoint := range []string{"https://127.0.0.1:8080", "http://example.com:8080", "http://127.0.0.1:8080/path"} {
		if _, err := NewControlClient(endpoint, nil); err == nil {
			t.Errorf("NewControlClient(%q) error = nil", endpoint)
		}
	}
}

func floatPointer(value float64) *float64 { return &value }

// NewHandler builds a read-only observer handler without Session mutations.
func NewHandler(source Source, token string) (http.Handler, error) {
	return newHandler(source, token, "")
}

type appBuildSource struct {
	sourceFunc
	builds map[string][]AppBuild
}

func (s appBuildSource) AppBuilds(_ context.Context, id string) ([]AppBuild, int, error) {
	builds, ok := s.builds[id]
	if !ok {
		return nil, http.StatusNotFound, errors.New("not found")
	}
	return builds, http.StatusOK, nil
}

func TestHandlerReturnsAuthenticatedAppBuilds(t *testing.T) {
	token := strings.Repeat("b", 32)
	source := appBuildSource{builds: map[string][]AppBuild{"app_one": {{Version: 3, ID: "sha256:" + strings.Repeat("a", 64)}}}}
	handler, err := NewHandler(source, token)
	if err != nil {
		t.Fatal(err)
	}
	get := func(path, bearer string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer "+bearer)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	response := get("/apps/app_one/builds", token)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"version":3`) {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	missing := get("/apps/app_missing/builds", token)
	var missingDocument struct {
		AppID string `json:"app_id"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if missing.Code != http.StatusNotFound || missing.Header().Get("Content-Type") != "application/json" ||
		json.Unmarshal(missing.Body.Bytes(), &missingDocument) != nil || missingDocument.AppID != "app_missing" ||
		missingDocument.Error.Code != "app_not_found" {
		t.Fatalf("missing App response = %d %s", missing.Code, missing.Body.String())
	}
	for path, status := range map[string]int{
		"/apps/../builds":          http.StatusBadRequest,
		"/apps/app_one/builds?x=1": http.StatusBadRequest,
	} {
		if got := get(path, token); got.Code != status {
			t.Errorf("%s = %d, want %d", path, got.Code, status)
		}
	}
	if got := get("/apps/app_one/builds", strings.Repeat("x", 32)); got.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token = %d", got.Code)
	}
}

func TestControlClientReadsAppBuilds(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/apps/app_one" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		body := `{"id":"app_one","slug":"one","builds":[{"version":2,"id":"sha256:` + strings.Repeat("a", 64) + `","created_at":"2026-10-14T00:00:00Z"}]}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	client, err := NewControlClient("http://127.0.0.1:8080", &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	builds, status, err := client.AppBuilds(context.Background(), "app_one")
	if err != nil || status != http.StatusOK || len(builds) != 1 || builds[0].Version != 2 {
		t.Fatalf("AppBuilds() = %#v, %d, %v", builds, status, err)
	}
}
