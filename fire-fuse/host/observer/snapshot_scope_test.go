package observer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fanOutTransport serves a host with several Apps and Workspaces, records
// every control-plane request, and tracks how many were in flight at once.
type fanOutTransport struct {
	apps          int
	workspaces    int
	inFlight      atomic.Int32
	maxInFlight   atomic.Int32
	mu            sync.Mutex
	requests      []string
	failWorkspace string
}

func workspaceIDFor(app, workspace int) string {
	return fmt.Sprintf("wsp_%d_%d", app, workspace)
}

func (f *fanOutTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	current := f.inFlight.Add(1)
	defer f.inFlight.Add(-1)
	for {
		seen := f.maxInFlight.Load()
		if current <= seen || f.maxInFlight.CompareAndSwap(seen, current) {
			break
		}
	}
	f.mu.Lock()
	f.requests = append(f.requests, r.URL.RequestURI())
	f.mu.Unlock()
	// Hold each request briefly so concurrent reads overlap.
	select {
	case <-time.After(5 * time.Millisecond):
	case <-r.Context().Done():
		return nil, r.Context().Err()
	}
	body := `{"status":"ready"}`
	switch r.URL.Path {
	case "/v1/host-capacity":
		body = `{"warm_slots":{"target":4,"ready":4,"claimed":0,"preparing":0}}`
	case "/v1/apps":
		items := make([]string, f.apps)
		for i := range items {
			items[i] = fmt.Sprintf(`{"id":"app-%d"}`, i)
		}
		body = `{"items":[` + strings.Join(items, ",") + `]}`
	case "/v1/workspaces":
		var app int
		if _, err := fmt.Sscanf(r.URL.Query().Get("app_id"), "app-%d", &app); err != nil {
			return nil, err
		}
		items := make([]string, f.workspaces)
		for i := range items {
			items[i] = fmt.Sprintf(`{"id":%q,"app_id":"app-%d","slug":"w%d"}`, workspaceIDFor(app, i), app, i)
		}
		body = `{"items":[` + strings.Join(items, ",") + `]}`
	case "/v1/sessions":
		workspace := r.URL.Query().Get("workspace_id")
		if workspace == f.failWorkspace {
			return &http.Response{StatusCode: http.StatusInternalServerError, Body: io.NopCloser(strings.NewReader("{}")), Header: make(http.Header)}, nil
		}
		body = fmt.Sprintf(`{"items":[{"id":"ses_%s","workspace_id":%q,"state":"active"}]}`, workspace, workspace)
	case "/v1/sandboxes":
		items := make([]string, 0, f.apps*f.workspaces)
		for app := 0; app < f.apps; app++ {
			for workspace := 0; workspace < f.workspaces; workspace++ {
				items = append(items, fmt.Sprintf(`{"id":"sbx_%d_%d","workspace_id":%q,"app_id":"app-%d"}`,
					app, workspace, workspaceIDFor(app, workspace), app))
			}
		}
		body = `{"items":[` + strings.Join(items, ",") + `]}`
	case "/v1/sandbox-usage":
		body = `{"observed_at":"2026-08-04T00:01:01Z","items":[{"sandbox_id":"sbx_0_1","cpu_usage_us":7}]}`
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
}

func (f *fanOutTransport) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	total := 0
	for _, request := range f.requests {
		if strings.SplitN(request, "?", 2)[0] == path {
			total++
		}
	}
	return total
}

func newFanOutClient(t *testing.T, transport *fanOutTransport) *ControlClient {
	t.Helper()
	client, err := NewControlClient("http://127.0.0.1:8080", &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestControlClientFansOutSnapshotReadsWithinBound(t *testing.T) {
	transport := &fanOutTransport{apps: 6, workspaces: 4}
	snapshot, err := newFanOutClient(t, transport).Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := transport.maxInFlight.Load(); got < 2 || got > controlReadConcurrency {
		t.Fatalf("max in-flight control reads = %d, want 2..%d", got, controlReadConcurrency)
	}
	if transport.count("/v1/workspaces") != 6 || transport.count("/v1/sessions") != 24 {
		t.Fatalf("requests = %v", transport.requests)
	}
	// Records keep the App-then-Workspace order the sequential read produced.
	if len(snapshot.Workspaces) != 24 || len(snapshot.Sessions) != 24 || len(snapshot.Sandboxes) != 24 {
		t.Fatalf("snapshot sizes = %d/%d/%d", len(snapshot.Workspaces), len(snapshot.Sessions), len(snapshot.Sandboxes))
	}
	for i, workspace := range snapshot.Workspaces {
		want := workspaceIDFor(i/4, i%4)
		if workspace.ID != want || snapshot.Sessions[i].WorkspaceID != want ||
			snapshot.Sessions[i].AppID != fmt.Sprintf("app-%d", i/4) {
			t.Fatalf("record %d = %#v / %#v, want %s", i, workspace, snapshot.Sessions[i], want)
		}
	}
	if snapshot.Sandboxes[1].Usage == nil || snapshot.Sandboxes[1].Usage.CPUUsageUS != 7 || snapshot.Sandboxes[0].Usage != nil {
		t.Fatalf("sandbox usage = %#v", snapshot.Sandboxes[:2])
	}
}

func TestControlClientScopesSnapshotToNamedWorkspaces(t *testing.T) {
	transport := &fanOutTransport{apps: 3, workspaces: 3}
	wanted := []string{workspaceIDFor(1, 2), workspaceIDFor(0, 1), "wsp_unknown"}
	snapshot, err := newFanOutClient(t, transport).WorkspaceSnapshot(context.Background(), wanted)
	if err != nil {
		t.Fatal(err)
	}
	if transport.count("/v1/sessions") != 2 {
		t.Fatalf("Session reads = %v", transport.requests)
	}
	ids := func(records int, id func(int) string) []string {
		out := make([]string, records)
		for i := range out {
			out[i] = id(i)
		}
		return out
	}
	want := []string{workspaceIDFor(0, 1), workspaceIDFor(1, 2)}
	gotWorkspaces := ids(len(snapshot.Workspaces), func(i int) string { return snapshot.Workspaces[i].ID })
	gotSessions := ids(len(snapshot.Sessions), func(i int) string { return snapshot.Sessions[i].WorkspaceID })
	gotSandboxes := ids(len(snapshot.Sandboxes), func(i int) string { return snapshot.Sandboxes[i].WorkspaceID })
	for _, got := range [][]string{gotWorkspaces, gotSessions, gotSandboxes} {
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("scoped snapshot = %v / %v / %v, want %v", gotWorkspaces, gotSessions, gotSandboxes, want)
		}
	}
	if snapshot.Sandboxes[0].Usage == nil || snapshot.Sandboxes[0].Usage.CPUUsageUS != 7 {
		t.Fatalf("scoped Sandbox usage = %#v", snapshot.Sandboxes[0])
	}
}

func TestControlClientEmptyScopeReadsOnlyHostState(t *testing.T) {
	transport := &fanOutTransport{apps: 3, workspaces: 3}
	snapshot, err := newFanOutClient(t, transport).WorkspaceSnapshot(context.Background(), []string{})
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Ready || snapshot.Workspaces == nil || len(snapshot.Workspaces) != 0 ||
		snapshot.Sessions == nil || len(snapshot.Sessions) != 0 || snapshot.Sandboxes == nil || len(snapshot.Sandboxes) != 0 {
		t.Fatalf("empty scope snapshot = %#v", snapshot)
	}
	for _, path := range []string{"/v1/apps", "/v1/workspaces", "/v1/sessions", "/v1/sandboxes", "/v1/sandbox-usage"} {
		if transport.count(path) != 0 {
			t.Fatalf("empty scope read %s: %v", path, transport.requests)
		}
	}
}

func TestControlClientSnapshotFailsWhenAnyFanOutReadFails(t *testing.T) {
	transport := &fanOutTransport{apps: 4, workspaces: 4, failWorkspace: workspaceIDFor(2, 3)}
	if _, err := newFanOutClient(t, transport).Snapshot(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("Snapshot() error = %v", err)
	}
}

func TestForEachBoundedCancelsAfterFirstError(t *testing.T) {
	failure := errors.New("boom")
	var started atomic.Int32
	err := forEachBounded(context.Background(), 50, 2, func(ctx context.Context, i int) error {
		started.Add(1)
		if i == 0 {
			return failure
		}
		<-ctx.Done()
		return ctx.Err()
	})
	if !errors.Is(err, failure) {
		t.Fatalf("forEachBounded() error = %v", err)
	}
	if got := started.Load(); got > 3 {
		t.Fatalf("started %d calls after the first failure", got)
	}
}

// scopedSource ignores the scope it is given, standing in for a Source whose
// upstream filter is broken.
type scopedSource struct {
	snapshot Snapshot
	scope    []string
}

func (s *scopedSource) Snapshot(context.Context) (Snapshot, error) { return s.snapshot, nil }

func (s *scopedSource) WorkspaceSnapshot(_ context.Context, ids []string) (Snapshot, error) {
	s.scope = ids
	return s.snapshot, nil
}

func TestHandlerScopesSnapshotToRequestedWorkspaces(t *testing.T) {
	token := strings.Repeat("s", 32)
	full := Snapshot{
		Ready:      true,
		Workspaces: []Workspace{{ID: "wsp_one", AppID: "app-1"}, {ID: "wsp_two", AppID: "app-2"}},
		Sessions:   []Session{{ID: "ses_one", WorkspaceID: "wsp_one"}, {ID: "ses_two", WorkspaceID: "wsp_two"}},
		Sandboxes:  []Sandbox{{ID: "sbx_one", WorkspaceID: "wsp_one"}, {ID: "sbx_two", WorkspaceID: "wsp_two"}},
	}
	read := func(t *testing.T, source Source, query string) (int, Snapshot) {
		t.Helper()
		handler, err := NewHandler(source, token)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodGet, "/snapshot"+query, nil)
		request.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		var snapshot Snapshot
		if response.Code == http.StatusOK {
			if err := json.NewDecoder(response.Body).Decode(&snapshot); err != nil {
				t.Fatal(err)
			}
		}
		return response.Code, snapshot
	}
	onlyOne := func(t *testing.T, snapshot Snapshot) {
		t.Helper()
		if len(snapshot.Workspaces) != 1 || snapshot.Workspaces[0].ID != "wsp_one" ||
			len(snapshot.Sessions) != 1 || snapshot.Sessions[0].ID != "ses_one" ||
			len(snapshot.Sandboxes) != 1 || snapshot.Sandboxes[0].ID != "sbx_one" {
			t.Fatalf("scoped snapshot = %#v", snapshot)
		}
	}

	t.Run("source without scoped reads", func(t *testing.T) {
		status, snapshot := read(t, sourceFunc(func(context.Context) (Snapshot, error) { return full, nil }), "?workspace_ids=wsp_one")
		if status != http.StatusOK {
			t.Fatalf("status = %d", status)
		}
		onlyOne(t, snapshot)
	})
	t.Run("scoped source is filtered again", func(t *testing.T) {
		source := &scopedSource{snapshot: full}
		status, snapshot := read(t, source, "?workspace_ids=wsp_one,wsp_three")
		if status != http.StatusOK || strings.Join(source.scope, ",") != "wsp_one,wsp_three" {
			t.Fatalf("status = %d, scope = %v", status, source.scope)
		}
		onlyOne(t, snapshot)
	})
	t.Run("empty scope", func(t *testing.T) {
		status, snapshot := read(t, &scopedSource{snapshot: full}, "?workspace_ids=")
		if status != http.StatusOK || !snapshot.Ready || snapshot.Workspaces == nil || len(snapshot.Workspaces) != 0 ||
			len(snapshot.Sessions) != 0 || len(snapshot.Sandboxes) != 0 {
			t.Fatalf("empty scope = %d %#v", status, snapshot)
		}
	})
	t.Run("unscoped operator view", func(t *testing.T) {
		status, snapshot := read(t, &scopedSource{snapshot: full}, "")
		if status != http.StatusOK || len(snapshot.Workspaces) != 2 || len(snapshot.Sandboxes) != 2 {
			t.Fatalf("unscoped = %d %#v", status, snapshot)
		}
	})
	tooMany := make([]string, maxSnapshotWorkspaces+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("wsp_%d", i)
	}
	for _, query := range []string{
		"?workspace_id=wsp_one",
		"?workspace_ids=wsp_one&workspace_ids=wsp_two",
		"?workspace_ids=wsp_one,,wsp_two",
		"?workspace_ids=app_one",
		"?workspace_ids=" + url.QueryEscape("wsp_one,wsp_two/../x"),
		"?workspace_ids=" + strings.Join(tooMany, ","),
		"?workspace_ids=%zz",
	} {
		if status, _ := read(t, &scopedSource{snapshot: full}, query); status != http.StatusBadRequest {
			t.Fatalf("query %.60s status = %d, want 400", query, status)
		}
	}
}

func TestNetdataClientReadsHostChartsConcurrentlyInSpecOrder(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	var inFlight, maxInFlight atomic.Int32
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/api/v1/chart") {
			if r.URL.Path != "/api/v1/chart" {
				return netdataResponse(http.StatusNotFound, ""), nil
			}
			return netdataResponse(http.StatusOK, fmt.Sprintf(`{"last_entry":%d}`, now.Unix())), nil
		}
		current := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			seen := maxInFlight.Load()
			if current <= seen || maxInFlight.CompareAndSwap(seen, current) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		return netdataResponse(http.StatusOK, `{"labels":["time","value"],"data":[[1788199200,1]]}`), nil
	})
	client, err := NewNetdataClient("http://127.0.0.1:19999", &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	client.now = func() time.Time { return now }
	snapshot, err := client.Infrastructure(context.Background(), infrastructureRanges["1h"])
	if err != nil {
		t.Fatal(err)
	}
	if got := maxInFlight.Load(); got < 2 || got > netdataChartConcurrency {
		t.Fatalf("max in-flight chart reads = %d, want 2..%d", got, netdataChartConcurrency)
	}
	charts := snapshot.Hosts[0].Charts
	if len(charts) != len(infrastructureHostSpecs[0].Charts) {
		t.Fatalf("compute charts = %d", len(charts))
	}
	for i, spec := range infrastructureHostSpecs[0].Charts {
		if charts[i].ID != spec.ID {
			t.Fatalf("chart %d = %s, want %s", i, charts[i].ID, spec.ID)
		}
	}
}
