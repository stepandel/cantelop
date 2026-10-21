package devruntime

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLocalIntegrationReadNeverProvisions(t *testing.T) {
	factory := &fakeLocalSandboxFactory{}
	runtime, err := newLocalRuntime(localTestAppID, factory)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	runtime.workspaces[localTestWorkspaceID] = localWorkspace{ID: localTestWorkspaceID, Slug: "production", CreatedAt: time.Now()}
	runtime.slugs["production"] = localTestWorkspaceID
	for _, fixture := range []struct {
		path string
		code int
	}{{"sessions/thread/view?workspace_slug=production", 503}, {"sessions/thread/view?workspace_slug=missing", 404}, {"sessions/thread/messages/" + localTestMessageID + "?workspace_slug=production", 200}, {"sessions/thread/view?workspace_slug=production&workspace_id=" + localTestWorkspaceID, 400}} {
		response := httptest.NewRecorder()
		runtime.ServeHTTP(response, httptest.NewRequest("GET", localIntegrationPrefix+fixture.path, nil))
		if response.Code != fixture.code {
			t.Fatalf("%s %d %s", fixture.path, response.Code, response.Body)
		}
	}
	response := httptest.NewRecorder()
	runtime.ServeHTTP(response, httptest.NewRequest("POST", localIntegrationPrefix+"messages", io.NopCloser(strings.NewReader(`{"session":{"id":"thread","workspace_id":"`+localTestWorkspaceID+`","keep_alive_seconds":900},"message":{"id":"`+localTestMessageID+`","payload":{}}}`))))
	if response.Code != 202 {
		t.Fatalf("steer %d %s", response.Code, response.Body)
	}
	if factory.starts != 0 || len(runtime.messages) != 1 || len(runtime.workspaces) != 1 {
		t.Fatal("read provisioned work")
	}
}

func TestLocalScopedRequestRejectsMissingAndForeignResourcesWithoutProvisioning(t *testing.T) {
	factory := &fakeLocalSandboxFactory{}
	runtime, err := newLocalRuntime(localTestAppID, factory)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	runtime.workspaces[localTestWorkspaceID] = localWorkspace{ID: localTestWorkspaceID, Slug: "production", CreatedAt: time.Now()}
	runtime.slugs["production"] = localTestWorkspaceID
	runtime.sessions[localLogicalSessionID(localTestAppID, "foreign")] = &localSession{workspaceID: "wsp_" + strings.Repeat("f", 32)}
	for _, fixture := range []struct {
		path string
		code int
	}{
		{"sessions/missing/requests?workspace_slug=production", 404},
		{"sessions/foreign/requests?workspace_slug=production", 404},
		{"sessions/thread/requests?workspace_slug=missing", 404},
		{"sessions/thread/requests?workspace_slug=production&workspace_id=" + localTestWorkspaceID, 400},
	} {
		response := httptest.NewRecorder()
		runtime.ServeHTTP(response, httptest.NewRequest("POST", localIntegrationPrefix+fixture.path, strings.NewReader(`{}`)))
		if response.Code != fixture.code {
			t.Fatalf("%s: %d %s", fixture.path, response.Code, response.Body)
		}
	}
	if factory.starts != 0 || len(runtime.messages) != 0 || len(runtime.workspaces) != 1 || len(runtime.sessions) != 1 {
		t.Fatal("scoped request provisioned resources")
	}
}
