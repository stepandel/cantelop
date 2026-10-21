package httpapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
)

// newTestStore opens an empty SQLite state store that is closed when t ends.
func newTestStore(t *testing.T) *microvm.SQLiteStore {
	t.Helper()
	store, err := microvm.OpenSQLiteStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func httpTestSessionRuntime() microvm.AppSessionRuntime {
	return microvm.AppSessionRuntime{
		Runtime: microvm.AppRuntimeSpec{Profile: microvm.DefaultSandboxProfileName, InternalPort: 3000},
	}
}

func TestHealth(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)

	NewHandler(nil).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if got, want := recorder.Header().Get("Content-Type"), "application/json"; got != want {
		t.Errorf("Content-Type = %q, want %q", got, want)
	}
	if got, want := recorder.Body.String(), "{\"status\":\"ok\"}\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestReadiness(t *testing.T) {
	checkCalls := 0
	handler := NewHandlerWithReadiness(nil, func() error {
		checkCalls++
		return nil
	})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if got, want := recorder.Body.String(), "{\"status\":\"ready\"}\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
	if checkCalls != 1 {
		t.Errorf("readiness check calls = %d, want 1", checkCalls)
	}
}

func TestReadinessUnavailable(t *testing.T) {
	handler := NewHandlerWithReadiness(nil, func() error {
		return errors.New("KVM unavailable")
	})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
	if body := recorder.Body.String(); !strings.Contains(body, `"code":"not_ready"`) {
		t.Errorf("body = %s, want not_ready error", body)
	}
	if body := recorder.Body.String(); strings.Contains(body, "KVM unavailable") {
		t.Errorf("body leaks internal readiness detail: %s", body)
	}
}

func TestReadinessRequiresCheck(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)

	NewHandler(nil).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
}

func TestMissingCapabilitiesAnswerServiceUnavailable(t *testing.T) {
	handler := NewHandler(nil)
	for _, test := range []struct {
		method, path, code string
	}{
		{http.MethodGet, "/v1/apps", "not_ready"},
		{http.MethodGet, "/v1/workspaces?app_id=app", "workspace_store_unavailable"},
		{http.MethodGet, "/v1/apps/app/workspace-sync/wsp", "sync_unavailable"},
		{http.MethodPost, "/v1/sessions", "runtime_unavailable"},
		{http.MethodGet, "/v1/sessions/ses/route", "runtime_unavailable"},
		{http.MethodPost, "/v1/pinned-work-leases", "runtime_unavailable"},
		{http.MethodGet, "/v1/message-executions", "execution_unavailable"},
		{http.MethodPost, "/v1/message-deliveries", "message_queue_unavailable"},
		{http.MethodGet, "/v1/message-deliveries/msg/wait", "message_queue_unavailable"},
		{http.MethodPost, "/v1/message-deliveries/msg/fail", "message_queue_unavailable"},
		{http.MethodGet, "/v1/message-delivery-events", "event_store_unavailable"},
		{http.MethodGet, "/v1/sandboxes", "runtime_unavailable"},
		{http.MethodPost, "/v1/sandboxes/sbx/crash", "runtime_unavailable"},
		{http.MethodGet, "/v1/sandbox-usage", "runtime_unavailable"},
		{http.MethodGet, "/v1/host-capacity", "runtime_unavailable"},
		{http.MethodGet, "/v1/request-events", "event_store_unavailable"},
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(test.method, test.path, strings.NewReader(`{}`)))
		if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), `"code":"`+test.code+`"`) {
			t.Errorf("%s %s = %d %s, want 503 %s", test.method, test.path, recorder.Code, recorder.Body.String(), test.code)
		}
	}
	// Request validation still precedes the durable-view capability check.
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/session-view", nil))
	if recorder.Code != http.StatusBadRequest {
		t.Errorf("invalid session view request = %d, want 400", recorder.Code)
	}
}

func TestProductAuthorityCompatibilityRoutesAreAbsent(t *testing.T) {
	handler := NewHandler(microvm.NewService(newTestStore(t)))
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/v1/internal/users/resolve", strings.NewReader(`{}`)),
		httptest.NewRequest(http.MethodGet, "/v1/internal/admin/users", nil),
		httptest.NewRequest(http.MethodGet, "/v1/internal/users/usr_test/apps", nil),
		httptest.NewRequest(http.MethodPost, "/v1/internal/users/usr_test/workspaces", strings.NewReader(`{}`)),
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusNotFound {
			t.Errorf("%s %s status = %d, want 404", request.Method, request.URL.Path, recorder.Code)
		}
	}
}
