package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type syncSpy struct {
	calls          int
	app, workspace string
}

func (s *syncSpy) SyncWorkspace(_ context.Context, app, workspace string, r *http.Request) (*http.Response, error) {
	s.calls++
	s.app = app
	s.workspace = workspace
	body := `{"entries":[]}`
	return &http.Response{StatusCode: 200, ContentLength: int64(len(body)), Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
}
func TestWorkspaceSyncOwnershipBeforeFileAccess(t *testing.T) {
	app := "app_" + strings.Repeat("1", 32)
	workspace := "wsp_" + strings.Repeat("2", 32)
	control := &fakeControl{workspaces: []Workspace{testWorkspace(app, workspace, "project", 0, 600)}}
	h, err := newTestHandler(control, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	spy := &syncSpy{}
	h.syncFiles = spy
	url := "http://origin/__cantelop/integration/v2/workspaces/" + workspace + "/sync?op=scan"
	for _, candidate := range []struct {
		app    string
		status int
	}{{"app_" + strings.Repeat("3", 32), 404}, {app, 200}} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, trustedEdgeRequest("GET", url, candidate.app, "agent.cantelop.dev"))
		if w.Code != candidate.status {
			t.Fatal(w.Code, w.Body)
		}
	}
	if spy.calls != 1 || spy.app != app || spy.workspace != workspace || control.creates != 0 {
		t.Fatal("ownership check or no-compute contract failed")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", url, nil))
	if w.Code < 400 || spy.calls != 1 {
		t.Fatal("untrusted direct access")
	}
	control.workspaces[0].ArchivedAt = &control.workspaces[0].UpdatedAt
	w = httptest.NewRecorder()
	h.ServeHTTP(w, trustedEdgeRequest("GET", url, app, "agent.cantelop.dev"))
	if w.Code != 409 || spy.calls != 1 {
		t.Fatal("archived workspace accessed")
	}
}
