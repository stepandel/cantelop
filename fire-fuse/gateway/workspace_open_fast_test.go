package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fastWorkspaceDirectory struct {
	WorkspaceDirectory
	workspace Workspace
	err       error
}

func (d fastWorkspaceDirectory) ResolveAppWorkspace(context.Context, string, string) (Workspace, error) {
	return d.workspace, d.err
}

func TestWorkspaceOpenRegisteredSkipsProductControl(t *testing.T) {
	workspace := testWorkspace("app_0123456789abcdef0123456789abcdef", "wsp_0123456789abcdef0123456789abcdef", "production", 0, 0)
	c := &fakeControl{workspaces: []Workspace{workspace}, openWorkspaceFn: func(context.Context, string, string) (Workspace, error) {
		t.Fatal("registered Workspace used product control")
		return Workspace{}, nil
	}}
	h, err := newTestHandler(c, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := trustedEdgeRequest(http.MethodPost, "http://edge-origin.cantelop.dev/__cantelop/v1/workspaces/open", workspace.AppID, "agent.cantelop.dev")
	r.Body = io.NopCloser(strings.NewReader(`{"slug":"production"}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), workspace.ID) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}
func TestWorkspaceOpenFastPathFallbackAndValidation(t *testing.T) {
	workspace := testWorkspace("app_0123456789abcdef0123456789abcdef", "wsp_0123456789abcdef0123456789abcdef", "production", 0, 0)
	for _, kind := range []string{"missing", "archived", "outage", "wrong_app", "wrong_slug", "missing_id"} {
		t.Run(kind, func(t *testing.T) {
			c := &fakeControl{workspaces: []Workspace{workspace}}
			opens := 0
			c.openWorkspaceFn = func(context.Context, string, string) (Workspace, error) { opens++; return workspace, nil }
			h, err := newTestHandler(c, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			local := workspace
			var lookupErr error
			switch kind {
			case "missing":
				lookupErr = &ControlError{StatusCode: http.StatusNotFound}
			case "archived":
				now := time.Now()
				local.ArchivedAt = &now
			case "outage":
				lookupErr = errors.New("control unavailable")
			case "wrong_app":
				local.AppID = "app_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			case "wrong_slug":
				local.Slug = "other"
			case "missing_id":
				local.ID = ""
			}
			h.workspaces = fastWorkspaceDirectory{WorkspaceDirectory: c, workspace: local, err: lookupErr}
			r := trustedEdgeRequest(http.MethodPost, "http://edge-origin.cantelop.dev/__cantelop/v1/workspaces/open", workspace.AppID, "agent.cantelop.dev")
			r.Body = io.NopCloser(strings.NewReader(`{"slug":"production"}`))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			fallback := kind == "missing" || kind == "archived"
			if fallback {
				if opens != 1 || w.Code != http.StatusOK {
					t.Fatalf("fallback opens=%d status=%d", opens, w.Code)
				}
			} else if opens != 0 || w.Code != http.StatusServiceUnavailable {
				t.Fatalf("invalid lookup opens=%d status=%d", opens, w.Code)
			}
		})
	}
}
