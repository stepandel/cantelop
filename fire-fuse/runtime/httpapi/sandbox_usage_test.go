package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
)

type usageControlPlane struct {
	*microvm.Service
	usage []microvm.RuntimeUsage
	err   error
}

func (c usageControlPlane) ListUsage() ([]microvm.RuntimeUsage, error) {
	return c.usage, c.err
}

func TestListSandboxUsageReturnsLiveReadings(t *testing.T) {
	guestUsed := uint64(32 << 20)
	handler := NewHandler(usageControlPlane{
		Service: microvm.NewService(newTestStore(t)),
		usage: []microvm.RuntimeUsage{{
			SandboxID: "sandbox-1", CPUUsageUS: 1250, MemoryCurrentBytes: 64 << 20, GuestMemoryMiB: 768,
			GuestMemoryUsedBytes: &guestUsed,
		}},
	})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/sandbox-usage", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var body sandboxUsageResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.ObservedAt.IsZero() || len(body.Items) != 1 || body.Items[0].SandboxID != "sandbox-1" ||
		body.Items[0].CPUUsageUS != 1250 || body.Items[0].MemoryCurrentBytes != 64<<20 || body.Items[0].GuestMemoryMiB != 768 ||
		body.Items[0].GuestMemoryUsedBytes == nil || *body.Items[0].GuestMemoryUsedBytes != guestUsed {
		t.Fatalf("body = %#v", body)
	}
}

func TestListSandboxUsageReportsUnavailableSupervisor(t *testing.T) {
	handler := NewHandler(usageControlPlane{
		Service: microvm.NewService(newTestStore(t)),
		err:     microvm.ErrSupervisorUnavailable,
	})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/sandbox-usage", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}

	missing := httptest.NewRecorder()
	NewHandler(microvm.NewService(newTestStore(t))).ServeHTTP(
		missing, httptest.NewRequest(http.MethodGet, "/v1/sandbox-usage", nil),
	)
	if missing.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing reader status = %d", missing.Code)
	}
}

func TestListSandboxUsageReportsReadFailure(t *testing.T) {
	handler := NewHandler(usageControlPlane{
		Service: microvm.NewService(newTestStore(t)),
		err:     errors.New("read failed"),
	})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/sandbox-usage", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}
