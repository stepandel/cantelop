package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stepandel/cantelop/fire-fuse/edgeprotocol"
)

func TestComputePoolRoutingRequiresTrustedMetadata(t *testing.T) {
	token := strings.Repeat("a", 64)
	handler := func(code int) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) })
	}
	router := NewComputePoolHandler(handler(201), handler(202), token)
	for _, test := range []struct {
		pool, auth string
		expected   int
	}{
		{"", "", 201}, {"standard", token, 201}, {"managed", token, 202}, {"managed", "wrong", 401}, {"other", token, 400},
	} {
		r := httptest.NewRequest("POST", "/__cantelop/v1/workspaces/open", nil)
		if test.pool != "" {
			r.Header.Set(edgeprotocol.ComputePoolHeader, test.pool)
		}
		r.Header.Set(edgeprotocol.AuthorizationHeader, "Bearer "+test.auth)
		r.Header.Set(edgeprotocol.DispatchHeader, "1")
		r.Header.Set(edgeprotocol.RequestIDHeader, "req_"+strings.Repeat("b", 32))
		r.Header.Set(edgeprotocol.AppIDHeader, "app_"+strings.Repeat("c", 32))
		r.Header.Set(edgeprotocol.HostnameHeader, "agent.cantelop.dev")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != test.expected {
			t.Fatalf("pool=%q status=%d, want %d", test.pool, w.Code, test.expected)
		}
		if test.pool == "managed" && test.auth == token {
			w = httptest.NewRecorder()
			NewComputePoolHandler(handler(201), nil, token).ServeHTTP(w, r)
			if w.Code != 503 {
				t.Fatal("missing managed runtime did not fail closed")
			}
			r.Header.Add(edgeprotocol.ComputePoolHeader, "standard")
			w = httptest.NewRecorder()
			router.ServeHTTP(w, r)
			if w.Code != 400 {
				t.Fatal("duplicate pool metadata was accepted")
			}
		}
	}
}
