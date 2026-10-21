package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stepandel/cantelop/clients/cli/internal/auth"
	"github.com/stepandel/cantelop/clients/cli/internal/controlapi"
)

const operationalTestOrigin = "http://127.0.0.1:12345"
const operationalTestAppID = "app_0123456789abcdef0123456789abcdef"

func TestOperationalCommandsResolveTheProjectAppAndEmitJSON(t *testing.T) {
	project, credentials := operationalTestProject(t)
	requests := 0
	client := operationalTestClient(t, &requests, func(request *http.Request) string {
		if request.URL.Path != "/v1/apps/"+operationalTestAppID+"/deployments" {
			t.Fatalf("release request = %s %s", request.Method, request.URL.Path)
		}
		return fmt.Sprintf(`{"items":[{"id":"rel_0123456789abcdef0123456789abcdef","app_id":%q,"version":2,"status":"active","api_artifact_digest":"sha256:%s","session_runtime_artifact_digest":"sha256:%s","components":{"api":{"status":"succeeded"},"session_runtime":{"status":"succeeded"}},"created_at":"2026-08-17T12:00:00Z","updated_at":"2026-08-17T12:00:00Z","activated_at":"2026-08-17T12:00:00Z"}]}`,
			operationalTestAppID, strings.Repeat("a", 64), strings.Repeat("b", 64))
	})
	t.Setenv("CANTELOP_CONFIG", credentials)
	var output bytes.Buffer
	if err := runReleases(context.Background(), []string{"--json", "--config", project}, &output, client); err != nil {
		t.Fatal(err)
	}
	var page releasePage
	if json.Unmarshal(output.Bytes(), &page) != nil || len(page.Items) != 1 || page.Items[0].Version != 2 {
		t.Fatalf("JSON output = %q", output.String())
	}
	if requests != 3 {
		t.Fatalf("requests = %d, want token, App resolution, and releases", requests)
	}
}

func TestSessionTerminateAcceptsFlagsAfterTheSessionID(t *testing.T) {
	project, credentials := operationalTestProject(t)
	requests := 0
	const sessionID = "ses_example"
	client := operationalTestClient(t, &requests, func(request *http.Request) string {
		if request.Method != http.MethodDelete || request.URL.Path != "/v1/apps/"+operationalTestAppID+"/sessions/"+sessionID {
			t.Fatalf("termination request = %s %s", request.Method, request.URL.Path)
		}
		return fmt.Sprintf(`{"id":%q,"workspace_id":"wsp_example","app_id":%q,"build_id":"build_example","keep_alive_seconds":3600,"state":"idle","created_at":"2026-08-17T12:00:00Z","updated_at":"2026-08-17T12:01:00Z"}`,
			sessionID, operationalTestAppID)
	})
	t.Setenv("CANTELOP_CONFIG", credentials)
	var output bytes.Buffer
	if err := runSessionAction(context.Background(), "terminate", []string{sessionID, "--json", "--config", project}, &output, client); err != nil {
		t.Fatal(err)
	}
	var session sessionDocument
	if json.Unmarshal(output.Bytes(), &session) != nil || session.ID != sessionID {
		t.Fatalf("JSON output = %q", output.String())
	}
}

func TestLogsPrintDurableRequestEvents(t *testing.T) {
	project, credentials := operationalTestProject(t)
	requests := 0
	client := operationalTestClient(t, &requests, func(request *http.Request) string {
		if request.URL.Path != "/v1/apps/"+operationalTestAppID+"/logs" || request.URL.Query().Get("limit") != "500" {
			t.Fatalf("logs request = %s", request.URL.String())
		}
		return `{"items":[{"sequence":7,"event_id":"evt_7","workspace_id":"wsp_example","session_id":"ses_example","received_at":"2026-08-17T12:00:00Z","status_code":202,"duration_ms":4.5}]}`
	})
	t.Setenv("CANTELOP_CONFIG", credentials)
	var output bytes.Buffer
	if err := runLogs(context.Background(), []string{"--config=" + project}, &output, client); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "202  4.5ms  ses_example  wsp_example") {
		t.Fatalf("logs output = %q", output.String())
	}
}

func operationalTestProject(t *testing.T) (string, string) {
	t.Helper()
	root := projectTestRoot(t)
	manifest := writeProjectManifest(t, root, `{"app":"vera","api":"src/api.ts","session":"src/session.ts"}`)
	credentials := filepath.Join(t.TempDir(), "credentials.json")
	if err := auth.SaveCredentials(credentials, controlapi.Credentials{
		ConsoleURL: operationalTestOrigin, ControlURL: operationalTestOrigin, RefreshToken: "refresh-token",
	}); err != nil {
		t.Fatal(err)
	}
	return manifest, credentials
}

func operationalTestClient(t *testing.T, requests *int, operation func(*http.Request) string) *http.Client {
	t.Helper()
	return &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		*requests++
		response := ""
		switch *requests {
		case 1:
			if request.URL.Path != "/api/cli/token" {
				t.Fatalf("token request = %s", request.URL.Path)
			}
			response = `{"access_token":"tenant-jwt","token_type":"Bearer","expires_in":60,"control_url":"` + operationalTestOrigin + `"}`
		case 2:
			if request.URL.Path != "/v1/apps/by-slug/vera" {
				t.Fatalf("App request = %s", request.URL.Path)
			}
			response = `{"id":"` + operationalTestAppID + `","slug":"vera"}`
		default:
			response = operation(request)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(response)), Header: make(http.Header)}, nil
	})}
}
