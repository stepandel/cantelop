package gateway

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeEnvironment struct {
	app   string
	calls int
	err   error
}

func (f *fakeEnvironment) UpdateEnvironment(_ context.Context, app, action string, input EnvironmentRequest) (EnvironmentUpdate, error) {
	f.app = app
	f.calls++
	if f.err != nil {
		return EnvironmentUpdate{}, f.err
	}
	return EnvironmentUpdate{Revision: 3, Status: "pending"}, nil
}

func TestEnvironmentUpdatesUseTrustedAppAndNeverProvisionSandbox(t *testing.T) {
	h, err := newTestHandler(&fakeControl{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeEnvironment{}
	h.environment = f
	input := `{"type":"app.environment.set","name":"GH_TOKEN","value":"fixture-token","secret":true}`
	r := trustedEdgeRequest("POST", "http://origin/__cantelop/v1/environment/set", lifecycleApp, "agent.cantelop.dev")
	r.Body = io.NopCloser(strings.NewReader(input))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || f.app != lifecycleApp || f.calls != 1 {
		t.Fatalf("%d %s app=%s calls=%d", w.Code, w.Body, f.app, f.calls)
	}
	if strings.Contains(w.Body.String(), "fixture-token") || strings.Contains(w.Body.String(), "value") {
		t.Fatal("secret returned")
	}
	for _, body := range []string{
		`{"type":"app.environment.set","app_id":"foreign","name":"GH_TOKEN","value":"token","secret":true}`,
		`{"type":"app.environment.set","name":"CANTELOP_INTEGRATION_TOKEN","value":"token","secret":true}`,
		`{"type":"app.environment.set","name":"GH_TOKEN","secret":true}`,
	} {
		r = trustedEdgeRequest("POST", "http://origin/__cantelop/v1/environment/set", lifecycleApp, "agent.cantelop.dev")
		r.Body = io.NopCloser(strings.NewReader(body))
		w = httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatalf("accepted invalid input: %d", w.Code)
		}
	}
	if f.calls != 1 {
		t.Fatal("invalid mutation reached product control")
	}
	r = httptest.NewRequest("POST", "http://origin/__cantelop/v1/environment/set", strings.NewReader(input))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code == 200 || f.calls != 1 {
		t.Fatal("accepted untrusted mutation")
	}
	f.err = &ControlError{StatusCode: 429}
	r = trustedEdgeRequest("POST", "http://origin/__cantelop/v1/environment/set", lifecycleApp, "agent.cantelop.dev")
	r.Body = io.NopCloser(strings.NewReader(input))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 429 {
		t.Fatalf("quota rejection reported as %d", w.Code)
	}
}
