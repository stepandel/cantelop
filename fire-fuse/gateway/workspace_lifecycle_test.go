package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const lifecycleApp = "app_0123456789abcdef0123456789abcdef"
const lifecycleWorkspace = "wsp_0123456789abcdef0123456789abcdef"

type lifecycleDeleter struct {
	WorkspaceCreator
	app, id string
	result  WorkspaceDeletion
	err     error
	calls   int
}

func (d *lifecycleDeleter) DeleteWorkspace(_ context.Context, app, id string) (WorkspaceDeletion, error) {
	d.calls++
	d.app = app
	d.id = id
	return d.result, d.err
}

func TestWorkspaceInspectionNeverProvisionsAndConcealsForeignOrArchivedWorkspaces(t *testing.T) {
	for _, kind := range []string{"present", "missing", "foreign", "archived", "outage"} {
		t.Run(kind, func(t *testing.T) {
			workspace := testWorkspace(lifecycleApp, lifecycleWorkspace, "production", 0, 0)
			control := &fakeControl{workspaces: []Workspace{workspace}, openWorkspaceFn: func(context.Context, string, string) (Workspace, error) {
				t.Fatal("inspection provisioned a Workspace")
				return Workspace{}, nil
			}}
			h, err := newTestHandler(control, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			var lookupErr error
			switch kind {
			case "missing":
				lookupErr = &ControlError{StatusCode: 404}
			case "foreign":
				workspace.AppID = "app_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			case "archived":
				at := time.Now()
				workspace.ArchivedAt = &at
			case "outage":
				lookupErr = errors.New("down")
			}
			h.workspaces = fastWorkspaceDirectory{WorkspaceDirectory: control, workspace: workspace, err: lookupErr}
			r := trustedEdgeRequest("GET", "http://edge-origin.cantelop.dev"+integrationPrefix+"workspaces?workspace_slug=production", lifecycleApp, "agent.cantelop.dev")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if kind == "outage" {
				if w.Code != 503 {
					t.Fatal(w.Code)
				}
				return
			}
			var envelope struct {
				Workspace *struct{ ID string } `json:"workspace"`
			}
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &envelope) != nil {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
			if (envelope.Workspace != nil) != (kind == "present") {
				t.Fatal(w.Body)
			}
			if control.creates != 0 {
				t.Fatal("activated compute")
			}
		})
	}
}

func TestWorkspaceDeletionUsesTrustedAppAndCanonicalIDAndPreservesFailures(t *testing.T) {
	control := &fakeControl{}
	h, err := newTestHandler(control, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	deleter := &lifecycleDeleter{WorkspaceCreator: control, result: WorkspaceDeletion{ID: lifecycleWorkspace, Status: "deleting", HostArchived: false}}
	h.workspaceCreator = deleter
	for _, kind := range []string{"success", "foreign", "outage", "mismatched"} {
		deleter.err = nil
		deleter.result.ID = lifecycleWorkspace
		expected := 202
		switch kind {
		case "foreign":
			deleter.err = &ControlError{StatusCode: 404}
			expected = 404
		case "outage":
			deleter.err = errors.New("down")
			expected = 503
		case "mismatched":
			deleter.result.ID = "wsp_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			expected = 503
		}
		r := trustedEdgeRequest("DELETE", "http://edge-origin.cantelop.dev"+integrationPrefix+"workspaces/"+lifecycleWorkspace, lifecycleApp, "agent.cantelop.dev")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != expected || deleter.app != lifecycleApp || deleter.id != lifecycleWorkspace {
			t.Fatalf("%s: %d %s", kind, w.Code, w.Body)
		}
	}
	before := deleter.calls
	for suffix, expected := range map[string]int{
		"workspaces?workspace_slug=a&workspace_id=" + lifecycleWorkspace:   405,
		"workspaces?workspace_slug=a&workspace_slug=b":                     405,
		"workspaces/" + lifecycleWorkspace + "?workspace_slug=replacement": 400,
	} {
		r := trustedEdgeRequest("DELETE", "http://edge-origin.cantelop.dev"+integrationPrefix+suffix, lifecycleApp, "agent.cantelop.dev")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != expected {
			t.Fatalf("%s: %d", suffix, w.Code)
		}
	}
	r := trustedEdgeRequest("GET", "http://edge-origin.cantelop.dev"+integrationPrefix+"workspaces/"+lifecycleWorkspace+"?x=1", lifecycleApp, "agent.cantelop.dev")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 405 {
		t.Fatalf("GET deletion route: %d", w.Code)
	}
	if deleter.calls != before {
		t.Fatal("invalid selector reached deletion")
	}
}

func TestProductClientDeletesCanonicalWorkspaceWithinApp(t *testing.T) {
	client, err := NewProductClient("https://console.cantelop.dev", &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != "DELETE" || r.URL.Path != "/internal/v1/apps/"+lifecycleApp+"/workspaces/"+lifecycleWorkspace {
			t.Fatalf("%s %s", r.Method, r.URL)
		}
		return &http.Response{StatusCode: 202, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":"` + lifecycleWorkspace + `","status":"deleting","hostArchived":true}`))}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.DeleteWorkspace(context.Background(), lifecycleApp, lifecycleWorkspace)
	if err != nil || result.ID != lifecycleWorkspace || !result.HostArchived {
		t.Fatalf("%+v %v", result, err)
	}
	if _, err = client.DeleteWorkspace(context.Background(), lifecycleApp, "../other"); err == nil {
		t.Fatal("accepted invalid ID")
	}
}
