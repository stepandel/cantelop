package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
)

func TestAppResourceAPIUsesSessionRuntimeBoundary(t *testing.T) {
	store := newTestStore(t)
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	buildID := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := store.EnsureComputeTenant(microvm.ComputeTenant{ID: "usr_test", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	service := microvm.NewService(store)
	handler := NewHandler(service)

	sessionRuntime := httpTestSessionRuntime()
	sessionRuntime.ActiveBuildID = buildID
	app := microvm.App{ID: "editor", Slug: "editor", CreatedAt: now, UpdatedAt: now}
	workspace := microvm.Workspace{ID: "wsp_editor", AppID: app.ID, Slug: "production", CreatedAt: now, UpdatedAt: now}
	if err := store.CreateAppForComputeTenant("usr_test", app, sessionRuntime, microvm.AppBuild{AppID: app.ID, BuildID: buildID, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateWorkspace(workspace); err != nil {
		t.Fatal(err)
	}
	assertHTTPBodyContains(t, handler, http.MethodGet, "/v1/apps/editor", "", http.StatusOK,
		`"slug":"editor"`, `"active_build_id":"`+buildID+`"`, `"internal_port":3000`,
		`"builds":[{"version":1,"id":"`+buildID+`"`)
	assertHTTPBodyContains(t, handler, http.MethodGet, "/v1/apps", "", http.StatusOK,
		`"items":[{"id":"editor"`)
	for _, legacy := range []string{"/v1/images", "/v1/images/runtime", "/v1/images/runtime/apps"} {
		assertHTTPBodyContains(t, handler, http.MethodGet, legacy, "", http.StatusNotFound)
	}

	assertHTTPBodyContains(t, handler, http.MethodPost, "/v1/apps", `{}`, http.StatusMethodNotAllowed)
}

func TestLegacyVMRoutesAreRemoved(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/vms", nil)
	NewHandler(microvm.NewService(newTestStore(t))).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("legacy route status = %d, want 404", recorder.Code)
	}
}

func assertHTTPBodyContains(
	t *testing.T,
	handler http.Handler,
	method, path, body string,
	wantStatus int,
	wants ...string,
) {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	handler.ServeHTTP(recorder, request)
	if recorder.Code != wantStatus {
		t.Fatalf("%s %s status = %d, want %d; body = %s", method, path, recorder.Code, wantStatus, recorder.Body.String())
	}
	for _, want := range wants {
		if !strings.Contains(recorder.Body.String(), want) {
			t.Errorf("%s %s body = %s, want %q", method, path, recorder.Body.String(), want)
		}
	}
}
