package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
)

const httpTestImageID = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type creatorFunc func(context.Context, string, microvm.CreateSpec) (microvm.Sandbox, error)

func (f creatorFunc) CreateAndStartWithID(ctx context.Context, key string, spec microvm.CreateSpec) (microvm.Sandbox, error) {
	return f(ctx, key, spec)
}

func (f creatorFunc) Get(string) (microvm.Sandbox, error) {
	return microvm.Sandbox{}, microvm.ErrNotFound
}

func (f creatorFunc) List() ([]microvm.Sandbox, error) {
	return []microvm.Sandbox{}, nil
}

func (f creatorFunc) Terminate(context.Context, string) (microvm.Sandbox, error) {
	return microvm.Sandbox{}, microvm.ErrNotFound
}

func TestCreateSandbox(t *testing.T) {
	now := time.Date(2026, time.July, 20, 20, 0, 0, 0, time.UTC)
	wantSandbox := microvm.Sandbox{
		ID: "sandbox-test", AppID: "app-test", WorkspaceID: "project-123",
		Profile: "default", BuildID: httpTestImageID, VCPUs: 1, MemoryMiB: 256,
		State: microvm.StateRunning,
		Network: microvm.NetworkAssignment{
			GuestIP:   "172.16.0.2",
			GatewayIP: "172.16.0.1",
			Subnet:    "172.16.0.0/30",
			GuestMAC:  "02:fc:ac:10:00:02",
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	var received microvm.CreateSpec
	requestContext := context.WithValue(context.Background(), contextKey{}, "create request")
	handler := NewHandler(creatorFunc(func(ctx context.Context, key string, spec microvm.CreateSpec) (microvm.Sandbox, error) {
		if got := ctx.Value(contextKey{}); got != "create request" {
			t.Errorf("CreateAndStart() context value = %v, want request context value", got)
		}
		if got := RequestID(ctx); got == "" {
			t.Error("CreateAndStart() context has no request ID")
		}
		if key != "sandbox-test" {
			t.Errorf("sandbox ID = %q, want sandbox-test", key)
		}
		received = spec
		return wantSandbox, nil
	}))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/sandboxes",
		strings.NewReader(`{"id":"sandbox-test","app_id":"app-test","workspace_id":"wsp_project123"}`),
	).WithContext(requestContext)

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusAccepted, recorder.Body.String())
	}
	wantSpec := microvm.CreateSpec{AppID: "app-test", WorkspaceID: "wsp_project123"}
	if received != wantSpec {
		t.Errorf("CreateAndStart() spec = %#v, want %#v", received, wantSpec)
	}
	if got, want := recorder.Header().Get("Content-Type"), "application/json"; got != want {
		t.Errorf("Content-Type = %q, want %q", got, want)
	}
	if body := recorder.Body.String(); !strings.Contains(body, `"id":"sandbox-test"`) ||
		!strings.Contains(body, `"workspace_id":"project-123"`) ||
		!strings.Contains(body, `"build_id":"`+httpTestImageID+`"`) ||
		!strings.Contains(body, `"state":"running"`) ||
		!strings.Contains(body, `"network":{"guest_ip":"172.16.0.2","gateway_ip":"172.16.0.1","subnet":"172.16.0.0/30","guest_mac":"02:fc:ac:10:00:02"}`) {
		t.Errorf("body = %s, want sandbox and workspace identity, running state, and network assignment", body)
	}
}

func TestCreateSandboxMapsInvalidAppID(t *testing.T) {
	handler := NewHandler(creatorFunc(func(_ context.Context, _ string, spec microvm.CreateSpec) (microvm.Sandbox, error) {
		if spec.AppID != "../../escape" {
			t.Errorf("AppID = %q, want ../../escape", spec.AppID)
		}
		return microvm.Sandbox{}, microvm.ErrInvalidSpec
	}))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/sandboxes",
		strings.NewReader(`{"id":"sandbox-invalid-app","app_id":"../../escape"}`),
	)

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
}

func TestCreateSandboxRejectsOmittedAppID(t *testing.T) {
	handler := NewHandler(creatorFunc(func(_ context.Context, _ string, spec microvm.CreateSpec) (microvm.Sandbox, error) {
		if spec.AppID != "" {
			t.Errorf("CreateAndStart() AppID = %q, want empty", spec.AppID)
		}
		return microvm.Sandbox{}, microvm.ErrInvalidSpec
	}))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/sandboxes",
		strings.NewReader(`{"id":"sandbox-missing-app"}`),
	)

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
}

func TestCreateSandboxRejectsInvalidJSON(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "malformed", body: `{"app_id":`},
		{name: "unknown field", body: `{"id":"sandbox-test","app_id":"app-test","cpu":1}`},
		{name: "legacy shape fields", body: `{"id":"sandbox-test","app_id":"app-test","profile":"default","vcpus":1,"memory_mib":256}`},
		{name: "multiple values", body: `{}` + `{}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			called := false
			handler := NewHandler(creatorFunc(func(context.Context, string, microvm.CreateSpec) (microvm.Sandbox, error) {
				called = true
				return microvm.Sandbox{}, nil
			}))
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/v1/sandboxes", strings.NewReader(test.body))

			handler.ServeHTTP(recorder, request)

			if recorder.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
			}
			if called {
				t.Error("CreateAndStart() was called for invalid JSON")
			}
		})
	}
}

func TestCreateSandboxMapsServiceErrors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{name: "invalid spec", err: microvm.ErrInvalidSpec, wantStatus: http.StatusBadRequest, wantCode: "invalid_request"},
		{name: "unknown profile", err: microvm.ErrUnknownProfile, wantStatus: http.StatusBadRequest, wantCode: "unknown_profile"},
		{name: "unknown image", err: microvm.ErrUnknownImage, wantStatus: http.StatusBadRequest, wantCode: "unknown_image"},
		{name: "unsupported shape", err: microvm.ErrUnsupportedShape, wantStatus: http.StatusBadRequest, wantCode: "unsupported_shape"},
		{name: "admission limit", err: microvm.ErrAdmissionLimit, wantStatus: http.StatusTooManyRequests, wantCode: "admission_limit"},
		{name: "sandbox conflict", err: microvm.ErrSandboxConflict, wantStatus: http.StatusConflict, wantCode: "sandbox_conflict"},
		{name: "runtime failure", err: microvm.ErrRuntimeUnavailable, wantStatus: http.StatusServiceUnavailable, wantCode: "runtime_unavailable"},
		{name: "missing supervisor", err: microvm.ErrSupervisorUnavailable, wantStatus: http.StatusServiceUnavailable, wantCode: "runtime_unavailable"},
		{name: "state store failure", err: microvm.ErrStateStore, wantStatus: http.StatusInternalServerError, wantCode: "internal_error"},
		{
			name:       "state failure takes precedence",
			err:        errors.Join(microvm.ErrRuntimeUnavailable, microvm.ErrStateStore),
			wantStatus: http.StatusInternalServerError,
			wantCode:   "internal_error",
		},
		{name: "internal failure", err: errors.New("store unavailable"), wantStatus: http.StatusInternalServerError, wantCode: "internal_error"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := NewHandler(creatorFunc(func(context.Context, string, microvm.CreateSpec) (microvm.Sandbox, error) {
				return microvm.Sandbox{}, test.err
			}))
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(
				http.MethodPost,
				"/v1/sandboxes",
				strings.NewReader(`{"id":"sandbox-service-error","app_id":"app-test"}`),
			)

			handler.ServeHTTP(recorder, request)

			if recorder.Code != test.wantStatus {
				t.Errorf("status = %d, want %d", recorder.Code, test.wantStatus)
			}
			if body := recorder.Body.String(); !strings.Contains(body, `"code":"`+test.wantCode+`"`) {
				t.Errorf("body = %s, want error code %q", body, test.wantCode)
			}
		})
	}
}

func TestCreateSandboxUsesBodyID(t *testing.T) {
	handler := NewHandler(creatorFunc(func(_ context.Context, id string, _ microvm.CreateSpec) (microvm.Sandbox, error) {
		if id != "" {
			t.Fatalf("sandbox ID = %q, want empty", id)
		}
		return microvm.Sandbox{}, microvm.ErrInvalidSpec
	}))
	request := httptest.NewRequest(http.MethodPost, "/v1/sandboxes", strings.NewReader(
		`{"app_id":"app-test"}`,
	))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}

func TestSandboxResponseIncludesSanitizedFailure(t *testing.T) {
	response := newSandboxResponse(microvm.Sandbox{
		ID:    "vm-failed",
		State: microvm.StateFailed,
		Failure: microvm.Failure{
			Reason: microvm.FailureRuntimeExit,
			RuntimeExit: microvm.RuntimeExit{
				PID:      123,
				ExitCode: 17,
			},
		},
	})

	if response.Failure == nil || response.Failure.Reason != microvm.FailureRuntimeExit {
		t.Fatalf("Failure = %#v, want runtime exit", response.Failure)
	}
	if response.Failure.RuntimeExit == nil {
		t.Fatal("RuntimeExit is nil")
	}
	if response.Failure.RuntimeExit.PID != 123 || response.Failure.RuntimeExit.ExitCode == nil || *response.Failure.RuntimeExit.ExitCode != 17 {
		t.Errorf("RuntimeExit = %#v, want PID 123 and exit code 17", response.Failure.RuntimeExit)
	}
}

func TestSandboxResponseIncludesStartupMeasurement(t *testing.T) {
	completedAt := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	response := newSandboxResponse(microvm.Sandbox{
		ID: "vm-running", State: microvm.StateRunning,
		StartupCompletedAt: completedAt,
		StartupDuration:    1250 * time.Millisecond,
	})

	if response.Startup == nil {
		t.Fatal("Startup is nil")
	}
	if !response.Startup.CompletedAt.Equal(completedAt) || response.Startup.DurationMS != 1250 {
		t.Errorf("Startup = %#v, want completion %s and 1250ms", response.Startup, completedAt)
	}
}
