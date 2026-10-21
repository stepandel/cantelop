package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
)

type crashLifecycle struct {
	crash func(context.Context, string) (microvm.Sandbox, error)
}

func (l crashLifecycle) Crash(ctx context.Context, id string) (microvm.Sandbox, error) {
	return l.crash(ctx, id)
}

func TestCrashSandbox(t *testing.T) {
	var receivedID string
	handler := NewHandler(crashLifecycle{crash: func(ctx context.Context, id string) (microvm.Sandbox, error) {
		if RequestID(ctx) == "" {
			t.Error("Crash() context has no request ID")
		}
		receivedID = id
		return microvm.Sandbox{ID: id, State: microvm.StateFailed, Failure: microvm.Failure{
			Reason: microvm.FailureRuntimeExit,
		}}, nil
	}})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/sandboxes/vm-test/crash", nil))
	if recorder.Code != http.StatusOK || receivedID != "vm-test" {
		t.Fatalf("response = %d, ID = %q: %s", recorder.Code, receivedID, recorder.Body.String())
	}
	if body := recorder.Body.String(); !strings.Contains(body, `"state":"failed"`) ||
		!strings.Contains(body, `"reason":"runtime_exited"`) {
		t.Fatalf("body = %s, want runtime failure", body)
	}
}

func TestCrashSandboxMapsErrors(t *testing.T) {
	tests := []struct {
		err    error
		status int
		code   string
	}{
		{microvm.ErrNotFound, http.StatusNotFound, "not_found"},
		{microvm.ErrInvalidTransition, http.StatusConflict, "invalid_state"},
		{microvm.ErrRuntimeUnavailable, http.StatusServiceUnavailable, "runtime_unavailable"},
		{errors.New("unexpected"), http.StatusInternalServerError, "internal_error"},
	}
	for _, test := range tests {
		handler := NewHandler(crashLifecycle{crash: func(context.Context, string) (microvm.Sandbox, error) {
			return microvm.Sandbox{}, test.err
		}})
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/sandboxes/vm-test/crash", nil))
		if recorder.Code != test.status || !strings.Contains(recorder.Body.String(), `"code":"`+test.code+`"`) {
			t.Errorf("error %v response = %d %s", test.err, recorder.Code, recorder.Body.String())
		}
	}
}
