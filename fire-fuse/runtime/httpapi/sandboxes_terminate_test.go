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

type terminatorLifecycle struct {
	readerLifecycle
	terminate func(context.Context, string) (microvm.Sandbox, error)
}

func (l terminatorLifecycle) Terminate(ctx context.Context, id string) (microvm.Sandbox, error) {
	return l.terminate(ctx, id)
}

func TestTerminateSandbox(t *testing.T) {
	var receivedID string
	requestContext := context.WithValue(context.Background(), contextKey{}, "request")
	handler := NewHandler(terminatorLifecycle{
		readerLifecycle: readerLifecycle{
			get:  func(string) (microvm.Sandbox, error) { return microvm.Sandbox{}, microvm.ErrNotFound },
			list: func() ([]microvm.Sandbox, error) { return nil, nil },
		},
		terminate: func(ctx context.Context, id string) (microvm.Sandbox, error) {
			if got := ctx.Value(contextKey{}); got != "request" {
				t.Errorf("Terminate() context value = %v, want request context value", got)
			}
			if got := RequestID(ctx); got == "" {
				t.Error("Terminate() context has no request ID")
			}
			receivedID = id
			return microvm.Sandbox{ID: id, State: microvm.StateTerminated}, nil
		},
	})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodDelete, "/v1/sandboxes/vm-test", nil).WithContext(requestContext)

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if receivedID != "vm-test" {
		t.Errorf("Terminate() ID = %q, want vm-test", receivedID)
	}
	if body := recorder.Body.String(); !strings.Contains(body, `"state":"terminated"`) {
		t.Errorf("body = %s, want terminated state", body)
	}
}

func TestTerminateSandboxMapsErrors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{name: "not found", err: microvm.ErrNotFound, wantStatus: http.StatusNotFound, wantCode: "not_found"},
		{name: "invalid state", err: microvm.ErrInvalidTransition, wantStatus: http.StatusConflict, wantCode: "invalid_state"},
		{name: "runtime failure", err: microvm.ErrRuntimeUnavailable, wantStatus: http.StatusServiceUnavailable, wantCode: "runtime_unavailable"},
		{name: "internal failure", err: errors.New("store unavailable"), wantStatus: http.StatusInternalServerError, wantCode: "internal_error"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := NewHandler(terminatorLifecycle{
				readerLifecycle: readerLifecycle{
					get:  func(string) (microvm.Sandbox, error) { return microvm.Sandbox{}, microvm.ErrNotFound },
					list: func() ([]microvm.Sandbox, error) { return nil, nil },
				},
				terminate: func(context.Context, string) (microvm.Sandbox, error) {
					return microvm.Sandbox{}, test.err
				},
			})
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodDelete, "/v1/sandboxes/vm-test", nil)

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

type contextKey struct{}
