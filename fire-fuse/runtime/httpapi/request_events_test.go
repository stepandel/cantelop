package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/controlclient"
	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
)

func TestGatewayControlClientRecordsIntoSQLite(t *testing.T) {
	store, err := microvm.OpenSQLiteStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	wantTime := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	buildID := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := store.EnsureComputeTenant(microvm.ComputeTenant{ID: "usr_events", CreatedAt: wantTime}); err != nil {
		t.Fatal(err)
	}
	app := microvm.App{ID: "app-1", Slug: "events-app", CreatedAt: wantTime, UpdatedAt: wantTime}
	if err := createHTTPTestApp(store, "usr_events", app, "wsp_events", "events", buildID, wantTime); err != nil {
		t.Fatal(err)
	}
	workspaces, err := store.ListWorkspaces(app.ID)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("seed Workspaces = %#v, %v", workspaces, err)
	}
	service := microvm.NewService(store)
	server := httptest.NewServer(NewHandler(service))
	defer server.Close()
	client, err := controlclient.New(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err := client.RecordRequestEvent(context.Background(), controlclient.ProxyRequestEvent{
		EventID: "gateway-request-1", RequestID: "req_0123456789abcdef0123456789abcdef",
		WorkspaceID: workspaces[0].ID,
		ReceivedAt:  wantTime, StatusCode: http.StatusBadGateway, Duration: 2750 * time.Microsecond,
	}); err != nil {
		t.Fatal(err)
	}
	page, err := service.ListRequestEvents(microvm.RequestEventQuery{Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].EventID != "gateway-request-1" ||
		page.Items[0].RequestID != "req_0123456789abcdef0123456789abcdef" ||
		page.Items[0].StatusCode != http.StatusBadGateway || page.Items[0].Duration != 2750*time.Microsecond {
		t.Fatalf("stored page = %#v, %v", page, err)
	}
}

func TestRequestEventAPIRecordsReplaysAndLists(t *testing.T) {
	store, err := microvm.OpenSQLiteStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	workspaceID := seedRequestEventWorkspace(t, store)
	handler := NewHandler(microvm.NewService(store))
	body := `{"event_id":"request-1","workspace_id":"` + workspaceID + `",` +
		`"received_at":"2026-08-05T12:00:00.123456Z","status_code":200,"duration_ms":12.345}`

	created := httptest.NewRecorder()
	handler.ServeHTTP(created, httptest.NewRequest(http.MethodPost, "/v1/request-events", strings.NewReader(body)))
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", created.Code, created.Body.String())
	}
	var event requestEventResponse
	if err := json.NewDecoder(created.Body).Decode(&event); err != nil {
		t.Fatal(err)
	}
	if event.Sequence != 1 || event.DurationMS != 12.345 || event.WorkspaceID != workspaceID {
		t.Fatalf("created event = %#v", event)
	}

	replayed := httptest.NewRecorder()
	handler.ServeHTTP(replayed, httptest.NewRequest(http.MethodPost, "/v1/request-events", strings.NewReader(body)))
	if replayed.Code != http.StatusOK {
		t.Fatalf("replay status = %d, body = %s", replayed.Code, replayed.Body.String())
	}

	listed := httptest.NewRecorder()
	handler.ServeHTTP(listed, httptest.NewRequest(http.MethodGet, "/v1/request-events?workspace_id="+workspaceID+"&limit=1", nil))
	if listed.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", listed.Code, listed.Body.String())
	}
	var page requestEventListResponse
	if err := json.NewDecoder(listed.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0] != event || page.NextBeforeSequence != 0 {
		t.Fatalf("listed page = %#v", page)
	}

	for after, want := range map[string]int{"0": -1, "1": 0} {
		forward := httptest.NewRecorder()
		handler.ServeHTTP(forward, httptest.NewRequest(http.MethodGet, "/v1/request-events?workspace_id="+workspaceID+"&after_sequence="+after, nil))
		if want < 0 {
			if forward.Code != http.StatusBadRequest {
				t.Fatalf("after_sequence=%s status = %d", after, forward.Code)
			}
			continue
		}
		var forwardPage requestEventListResponse
		if forward.Code != http.StatusOK || json.NewDecoder(forward.Body).Decode(&forwardPage) != nil || len(forwardPage.Items) != want {
			t.Fatalf("after_sequence=%s = %d %#v", after, forward.Code, forwardPage)
		}
	}
}

func TestRequestEventAPIRejectsInvalidAndConflictingData(t *testing.T) {
	store, err := microvm.OpenSQLiteStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	workspaceID := seedRequestEventWorkspace(t, store)
	handler := NewHandler(microvm.NewService(store))
	valid := `{"event_id":"request-1","workspace_id":"` + workspaceID + `","received_at":"2026-08-05T12:00:00Z","status_code":200,"duration_ms":1}`
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodPost, "/v1/request-events", strings.NewReader(valid)))
	if first.Code != http.StatusCreated {
		t.Fatalf("initial status = %d", first.Code)
	}

	conflicting := httptest.NewRecorder()
	handler.ServeHTTP(conflicting, httptest.NewRequest(
		http.MethodPost, "/v1/request-events", strings.NewReader(strings.Replace(valid, `"status_code":200`, `"status_code":500`, 1)),
	))
	if conflicting.Code != http.StatusConflict {
		t.Fatalf("conflict status = %d, body = %s", conflicting.Code, conflicting.Body.String())
	}

	for _, target := range []string{
		"/v1/request-events?limit=501",
		"/v1/request-events?before_sequence=-1",
		"/v1/request-events?after_sequence=0",
		"/v1/request-events?before_sequence=3&after_sequence=1",
		"/v1/request-events?unknown=true",
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusBadRequest {
			t.Errorf("GET %s status = %d", target, response.Code)
		}
	}
}

func seedRequestEventWorkspace(t *testing.T, store *microvm.SQLiteStore) string {
	t.Helper()
	now := time.Date(2026, 8, 5, 11, 0, 0, 0, time.UTC)
	buildID := "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if _, err := store.EnsureComputeTenant(microvm.ComputeTenant{ID: "usr_request_events", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	app := microvm.App{ID: "request-events", Slug: "request-events", CreatedAt: now, UpdatedAt: now}
	if err := createHTTPTestApp(store, "usr_request_events", app, "wsp_request_events", "request-events", buildID, now); err != nil {
		t.Fatal(err)
	}
	workspaces, err := store.ListWorkspaces(app.ID)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("seed Workspaces = %#v, %v", workspaces, err)
	}
	return workspaces[0].ID
}

func createHTTPTestApp(store *microvm.SQLiteStore, userID string, app microvm.App, workspaceID, slug, buildID string, at time.Time) error {
	sessionRuntime := httpTestSessionRuntime()
	sessionRuntime.ActiveBuildID = buildID
	workspace := microvm.Workspace{ID: workspaceID, AppID: app.ID, Slug: slug, CreatedAt: at, UpdatedAt: at}
	build := microvm.AppBuild{AppID: app.ID, BuildID: buildID, CreatedAt: at}
	if err := store.CreateAppForComputeTenant(userID, app, sessionRuntime, build); err != nil {
		return err
	}
	return store.CreateWorkspace(workspace)
}
