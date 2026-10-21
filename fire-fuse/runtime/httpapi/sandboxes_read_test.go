package httpapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
)

type readerLifecycle struct {
	creatorFunc
	get  func(string) (microvm.Sandbox, error)
	list func() ([]microvm.Sandbox, error)
}

func (l readerLifecycle) Get(id string) (microvm.Sandbox, error) {
	return l.get(id)
}

func (l readerLifecycle) List() ([]microvm.Sandbox, error) {
	return l.list()
}

func TestGetSandbox(t *testing.T) {
	now := time.Date(2026, time.July, 20, 20, 0, 0, 0, time.UTC)
	want := microvm.Sandbox{
		ID:    "vm-test",
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
	var receivedID string
	handler := NewHandler(readerLifecycle{
		get: func(id string) (microvm.Sandbox, error) {
			receivedID = id
			return want, nil
		},
		list: func() ([]microvm.Sandbox, error) { return nil, nil },
	})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/sandboxes/vm-test", nil)

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if receivedID != "vm-test" {
		t.Errorf("Get() ID = %q, want vm-test", receivedID)
	}
	if body := recorder.Body.String(); !strings.Contains(body, `"id":"vm-test"`) ||
		!strings.Contains(body, `"guest_ip":"172.16.0.2"`) {
		t.Errorf("body = %s, want sandbox ID and guest IP", body)
	}
}

func TestGetSandboxMapsErrors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{name: "not found", err: microvm.ErrNotFound, wantStatus: http.StatusNotFound, wantCode: "not_found"},
		{name: "internal failure", err: errors.New("store unavailable"), wantStatus: http.StatusInternalServerError, wantCode: "internal_error"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := NewHandler(readerLifecycle{
				get:  func(string) (microvm.Sandbox, error) { return microvm.Sandbox{}, test.err },
				list: func() ([]microvm.Sandbox, error) { return nil, nil },
			})
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/v1/sandboxes/vm-missing", nil)

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

func TestListSandboxs(t *testing.T) {
	handler := NewHandler(readerLifecycle{
		get: func(string) (microvm.Sandbox, error) { return microvm.Sandbox{}, microvm.ErrNotFound },
		list: func() ([]microvm.Sandbox, error) {
			return []microvm.Sandbox{
				{
					ID:    "vm-a",
					State: microvm.StateRunning,
					Network: microvm.NetworkAssignment{
						GuestIP:   "172.16.0.2",
						GatewayIP: "172.16.0.1",
						Subnet:    "172.16.0.0/30",
						GuestMAC:  "02:fc:ac:10:00:02",
					},
				},
				{ID: "vm-b", State: microvm.StateStarting},
			}, nil
		},
	})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/sandboxes", nil)

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if body := recorder.Body.String(); !strings.Contains(body, `"items":[`) ||
		!strings.Contains(body, `"id":"vm-a"`) ||
		!strings.Contains(body, `"id":"vm-b"`) ||
		!strings.Contains(body, `"network":{"guest_ip":"172.16.0.2"`) ||
		strings.Count(body, `"network":`) != 1 {
		t.Errorf("body = %s, want both sandboxes and only vm-a's network assignment", body)
	}
}

func TestListSandboxsReturnsEmptyArray(t *testing.T) {
	handler := NewHandler(readerLifecycle{
		get:  func(string) (microvm.Sandbox, error) { return microvm.Sandbox{}, microvm.ErrNotFound },
		list: func() ([]microvm.Sandbox, error) { return nil, nil },
	})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/sandboxes", nil)

	handler.ServeHTTP(recorder, request)

	if got, want := recorder.Body.String(), "{\"items\":[]}\n"; got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
}

func TestListSandboxsMapsStorageError(t *testing.T) {
	handler := NewHandler(readerLifecycle{
		get:  func(string) (microvm.Sandbox, error) { return microvm.Sandbox{}, microvm.ErrNotFound },
		list: func() ([]microvm.Sandbox, error) { return nil, errors.New("store unavailable") },
	})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/sandboxes", nil)

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
	if body := recorder.Body.String(); !strings.Contains(body, `"code":"internal_error"`) {
		t.Errorf("body = %s, want internal_error", body)
	}
}
