package httpapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
)

type capacityReader struct {
	capacity microvm.HostCapacity
	err      error
}

func (r capacityReader) HostCapacity() (microvm.HostCapacity, error) {
	return r.capacity, r.err
}

func TestHostCapacityReturnsWarmSlotState(t *testing.T) {
	handler := NewHandler(capacityReader{capacity: microvm.HostCapacity{
		Target: 16, Ready: 11, Claimed: 4, Preparing: 1,
	}})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/host-capacity", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(),
		`"warm_slots":{"target":16,"ready":11,"claimed":4,"preparing":1}`) {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

func TestHostCapacityFailsClosedWhenUnavailable(t *testing.T) {
	for _, controlPlane := range []any{struct{}{}, capacityReader{err: errors.New("read failed")}} {
		response := httptest.NewRecorder()
		NewHandler(controlPlane).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/host-capacity", nil))
		if response.Code < 500 {
			t.Fatalf("response = %d %s", response.Code, response.Body.String())
		}
	}
}
