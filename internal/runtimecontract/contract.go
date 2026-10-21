// Package runtimecontract supplies the public SDK contract checks shared by the
// remote gateway and local development runtime. It is imported only by tests.
package runtimecontract

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	AppID       = "app_0123456789abcdef0123456789abcdef"
	WorkspaceID = "wsp_0123456789abcdef0123456789abcdef"
	MessageID   = "msg_0123456789abcdef0123456789abcdef"
	Body        = `{"session":{"id":"agent:primary","workspace_id":"` + WorkspaceID + `","keep_alive_seconds":30},"message":{"id":"` + MessageID + `","payload":{"command":"auth"}}}`
)

// Routes lists the request shapes SDKs send. The edge outbound Worker tests
// read the same file so all three runtimes agree on what is routable.
//
//go:embed routes.json
var routesDocument []byte

type Route struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	// Status is set only for rejected shapes: 404 or 405.
	Status int `json:"status,omitempty"`
}

type Seed struct {
	State, ErrorCode string
	Reply            json.RawMessage
}
type Factory func(*testing.T, Seed) http.Handler

func Run(t *testing.T, factory Factory) {
	requestBody := strings.TrimSuffix(Body, "}") + `,"timeout_ms":5}`
	cases := []struct {
		name, method, path, body, contentType string
		status                                int
		code                                  string
		seed                                  Seed
	}{
		{name: "dispatch", method: "POST", path: "/messages", body: Body, status: 202},
		{name: "json charset", method: "POST", path: "/messages", body: Body, contentType: "application/json; charset=utf-8", status: 202},
		{name: "invalid message", method: "POST", path: "/messages", body: `{}`, status: 400, code: "invalid_message_request"},
		{name: "unknown field", method: "POST", path: "/messages", body: strings.TrimSuffix(Body, "}") + `,"extra":1}`, status: 400, code: "invalid_message_request"},
		{name: "trailing JSON", method: "POST", path: "/messages", body: Body + ` {}`, status: 400, code: "invalid_message_request"},
		{name: "oversized whitespace", method: "POST", path: "/messages", body: Body + strings.Repeat(" ", 1<<20), status: 400, code: "invalid_message_request"},
		{name: "reply flag on message", method: "POST", path: "/messages", body: strings.TrimSuffix(Body, "}") + `,"reply":true}`, status: 400, code: "invalid_message_request"},
		{name: "request timeout", method: "POST", path: "/requests", body: requestBody, status: 504, code: "request_wait_timeout"},
		{name: "request missing timeout", method: "POST", path: "/requests", body: Body, status: 400, code: "invalid_request"},
		{name: "request invalid timeout", method: "POST", path: "/requests", body: strings.Replace(requestBody, `"timeout_ms":5`, `"timeout_ms":300001`, 1), status: 400, code: "invalid_request"},
		{name: "request reply", method: "POST", path: "/requests", body: requestBody, status: 200, seed: Seed{State: "handled", Reply: json.RawMessage(`{"ok":true}`)}},
		{name: "request null reply", method: "POST", path: "/requests", body: requestBody, status: 200, seed: Seed{State: "handled", Reply: json.RawMessage(`null`)}},
		{name: "request expired reply", method: "POST", path: "/requests", body: requestBody, status: 410, code: "request_reply_expired", seed: Seed{State: "handled"}},
		{name: "request handler failure", method: "POST", path: "/requests", body: requestBody, status: 422, code: "message_handler_failed", seed: Seed{State: "failed", ErrorCode: "message_handler_failed"}},
		{name: "request handler timeout", method: "POST", path: "/requests", body: requestBody, status: 422, code: "message_timed_out", seed: Seed{State: "failed", ErrorCode: "message_timed_out"}},
		{name: "message query", method: "POST", path: "/messages?extra=1", body: Body, status: 404, code: "runtime_route_not_found"},
		{name: "request query", method: "POST", path: "/requests?extra=1", body: requestBody, status: 404, code: "runtime_route_not_found"},
		{name: "unknown status", method: "GET", path: "/sessions/agent%3Aprimary/messages/" + MessageID, status: 200},
		{name: "event workspace required", method: "GET", path: "/sessions/thread/events", status: 400, code: "invalid_event_cursor"},
		{name: "event cursor identity required", method: "GET", path: "/sessions/thread/events?workspace_id=" + WorkspaceID + "&after=1", status: 400, code: "invalid_event_cursor"},
		{name: "event uppercase stream identity", method: "GET", path: "/sessions/thread/events?workspace_id=" + WorkspaceID + "&stream_id=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", status: 400, code: "invalid_event_cursor"},
		{name: "event duplicate cursor", method: "GET", path: "/sessions/thread/events?workspace_id=" + WorkspaceID + "&after=0&after=0", status: 400, code: "invalid_event_cursor"},
		{name: "event transport before stream lookup", method: "GET", path: "/sessions/thread/events?workspace_id=" + WorkspaceID + "&stream_id=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", status: 406, code: "event_transport_required"},

		{name: "invalid workspace", method: "POST", path: "/workspaces", body: `{"slug":"bad--slug"}`, status: 400, code: "invalid_workspace_request"},
		{name: "invalid session", method: "POST", path: "/sessions", body: `{}`, status: 400, code: "invalid_session_request"},
		{name: "missing keep alive", method: "POST", path: "/sessions/open", body: `{"key":"thread"}`, status: 400, code: "invalid_session_request"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := factory(t, tc.seed)
			req := httptest.NewRequest(tc.method, "/__cantelop/v1"+tc.path, strings.NewReader(tc.body))
			contentType := tc.contentType
			if contentType == "" {
				contentType = "application/json"
			}
			req.Header.Set("Content-Type", contentType)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			if response.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", response.Code, tc.status, response.Body)
			}
			var envelope struct {
				ID, Status, State string
				Reply             json.RawMessage
				Error             struct{ Code string }
			}
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Error.Code != tc.code {
				t.Fatalf("error = %q, want %q", envelope.Error.Code, tc.code)
			}
			if tc.status == 202 && (envelope.ID != MessageID || envelope.Status != "accepted") {
				t.Fatalf("receipt = %s", response.Body)
			}
			if len(tc.seed.Reply) > 0 && (envelope.ID != MessageID || string(envelope.Reply) != string(tc.seed.Reply)) {
				t.Fatalf("reply = %s", response.Body)
			}
			if tc.name == "unknown status" && (envelope.ID != MessageID || envelope.State != "unknown") {
				t.Fatalf("status = %s", response.Body)
			}
		})
	}
	var routes struct{ Routes, Rejects []Route }
	if err := json.Unmarshal(routesDocument, &routes); err != nil || len(routes.Routes) == 0 || len(routes.Rejects) == 0 {
		t.Fatalf("decode routes.json: %v", err)
	}
	for _, route := range routes.Routes {
		t.Run("route "+route.Method+" "+route.Path, func(t *testing.T) {
			handler := factory(t, Seed{})
			req := httptest.NewRequest(route.Method, route.Path, strings.NewReader("{}"))
			req.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			if response.Code == 405 || strings.Contains(response.Body.String(), "runtime_route_not_found") {
				t.Fatalf("not routed: %d %s", response.Code, response.Body)
			}
		})
	}
	for _, route := range routes.Rejects {
		t.Run("reject "+route.Method+" "+route.Path, func(t *testing.T) {
			handler := factory(t, Seed{})
			req := httptest.NewRequest(route.Method, route.Path, strings.NewReader("{}"))
			req.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			code := "runtime_route_not_found"
			if route.Status == http.StatusMethodNotAllowed {
				code = "runtime_method_not_allowed"
			}
			if response.Code != route.Status || !strings.Contains(response.Body.String(), code) {
				t.Fatalf("rejection = %d %s, want %d %s", response.Code, response.Body, route.Status, code)
			}
		})
	}
	for path, method := range map[string]string{"/messages": "POST", "/requests": "POST", "/workspaces": "POST", "/workspaces/open": "POST", "/workspaces/database/credentials": "POST", "/sessions": "POST", "/sessions/open": "POST", "/sessions/thread": "DELETE", "/sessions/thread/messages/" + MessageID: "GET", "/sessions/thread/events": "GET"} {
		t.Run("method "+path, func(t *testing.T) {
			handler := factory(t, Seed{})
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest("PATCH", "/__cantelop/v1"+path, nil))
			if response.Code != 405 || response.Header().Get("Allow") != method || !strings.Contains(response.Body.String(), "runtime_method_not_allowed") {
				t.Fatalf("method = %d %s %s", response.Code, response.Header(), response.Body)
			}
		})
	}
	t.Run("idempotency", func(t *testing.T) {
		handler := factory(t, Seed{})
		send := func(path, body string) *httptest.ResponseRecorder {
			r := httptest.NewRequest("POST", "/__cantelop/v1"+path, strings.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			return w
		}
		first := send("/messages", Body)
		again := send("/messages", Body)
		if first.Code != 202 || again.Code != 202 || first.Body.String() != again.Body.String() {
			t.Fatalf("duplicate = %d %s / %d %s", first.Code, first.Body, again.Code, again.Body)
		}
		for _, body := range []string{strings.Replace(Body, "auth", "steer", 1), strings.Replace(Body, "primary", "secondary", 1), strings.Replace(Body, ":30", ":31", 1)} {
			if got := send("/messages", body); got.Code != 409 {
				t.Fatalf("conflict = %d %s", got.Code, got.Body)
			}
		}
		if got := send("/requests", requestBody); got.Code != 409 {
			t.Fatalf("reply identity = %d %s", got.Code, got.Body)
		}
	})
}
