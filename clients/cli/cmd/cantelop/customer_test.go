package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stepandel/cantelop/clients/cli/internal/controlapi"
)

func TestAppCreatePersistsProvisioningAppWithoutStartingADeployment(t *testing.T) {
	const appID = "app_0123456789abcdef0123456789abcdef"
	sequence := 0
	const origin = "http://127.0.0.1:12345"
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		sequence++
		status := http.StatusOK
		var response string
		switch {
		case request.URL.Path == "/api/cli/token":
			if sequence != 1 {
				t.Errorf("token exchange sequence = %d", sequence)
			}
			response = fmt.Sprintf(`{"access_token":"tenant-jwt","token_type":"Bearer","expires_in":60,"control_url":%q}`, origin)
		case request.Method == http.MethodPost && request.URL.Path == "/v1/apps":
			if sequence != 2 {
				t.Errorf("App creation sequence = %d", sequence)
			}
			var input map[string]any
			if json.NewDecoder(request.Body).Decode(&input) != nil || input["build_id"] != nil || input["slug"] != "hello" ||
				input["workspace"] != nil {
				t.Errorf("App creation input = %#v", input)
			}
			sessionRuntime := input["session_runtime"].(map[string]any)
			runtime := sessionRuntime["runtime"].(map[string]any)
			if len(sessionRuntime) != 1 || len(runtime) != 1 || runtime["profile"] != "default" {
				t.Errorf("App runtime input = %#v, want only default profile", runtime)
			}
			status = http.StatusCreated
			response = fmt.Sprintf(`{"id":%q,"slug":"hello"}`, appID)
		default:
			status = http.StatusNotFound
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(response)), Header: make(http.Header)}, nil
	})}

	session := &controlapi.Session{Client: client, Credentials: controlapi.Credentials{
		ConsoleURL: origin, ControlURL: origin, RefreshToken: "cantelop-refresh",
	}}
	var output bytes.Buffer
	if err := runAppCreate(context.Background(), []string{"-slug", "hello"}, &output, session); err != nil {
		t.Fatalf("runAppCreate() error = %v", err)
	}
	if sequence != 2 || output.String() != "Created "+appID+"\n" {
		t.Fatalf("sequence=%d output=%q", sequence, output.String())
	}
}

func TestAppResourceCommandsMatchTheProductAPI(t *testing.T) {
	const (
		origin = "http://127.0.0.1:12345"
		appID  = "app_0123456789abcdef0123456789abcdef"
	)
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		status, response := http.StatusOK, ""
		switch requests {
		case 1:
			if request.Method != http.MethodGet || request.URL.Path != "/v1/apps" {
				t.Fatalf("list request = %s %s", request.Method, request.URL.Path)
			}
			response = fmt.Sprintf(`{"items":[{"id":%q,"slug":"hello"}]}`, appID)
		case 2:
			if request.Method != http.MethodGet || request.URL.Path != "/v1/apps/"+appID {
				t.Fatalf("inspect request = %s %s", request.Method, request.URL.Path)
			}
			response = fmt.Sprintf(`{"id":%q,"slug":"hello"}`, appID)
		case 3:
			if request.Method != http.MethodDelete || request.URL.Path != "/v1/apps/"+appID {
				t.Fatalf("archive request = %s %s", request.Method, request.URL.Path)
			}
			status = http.StatusAccepted
		default:
			t.Fatalf("unexpected request %s %s", request.Method, request.URL.Path)
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(response)), Header: make(http.Header)}, nil
	})}
	session := &controlapi.Session{
		Client: client, Credentials: controlapi.Credentials{ControlURL: origin},
		Token: controlapi.TokenResponse{AccessToken: "tenant-jwt"}, ExpiresAt: time.Now().Add(time.Hour),
	}
	var output bytes.Buffer
	if err := runAppList(context.Background(), nil, &output, session); err != nil || !strings.Contains(output.String(), appID+"\thello") {
		t.Fatalf("App list = %q, %v", output.String(), err)
	}
	output.Reset()
	if err := runAppInspect(context.Background(), []string{appID}, &output, session); err != nil || !strings.Contains(output.String(), "Slug: hello") {
		t.Fatalf("App inspect = %q, %v", output.String(), err)
	}
	output.Reset()
	if err := runAppArchive(context.Background(), []string{appID}, &output, session); err != nil || output.String() != "Archive requested for "+appID+"\n" {
		t.Fatalf("App archive = %q, %v", output.String(), err)
	}
	if requests != 3 {
		t.Fatalf("requests = %d", requests)
	}
}

func TestAppDeployUploadsOneBundleAndActivatesOneRelease(t *testing.T) {
	const (
		origin    = "http://127.0.0.1:12345"
		appID     = "app_0123456789abcdef0123456789abcdef"
		uploadID  = "upl_0123456789abcdef0123456789abcdef"
		releaseID = "rel_0123456789abcdef0123456789abcdef"
	)
	directory := t.TempDir()
	manifest := `{"schema_version":2,"kind":"cantelop-edge-api","main_module":"worker.mjs"}`
	module := []byte("export default { fetch() { return new Response('ok') } }")
	if err := os.WriteFile(filepath.Join(directory, "cantelop-api.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "worker.mjs"), module, 0o644); err != nil {
		t.Fatal(err)
	}
	dockerDirectory := t.TempDir()
	docker := filepath.Join(dockerDirectory, "docker")
	const imageID = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	dockerScript := `#!/bin/sh
if [ "$1" = image ] && [ "$2" = inspect ]; then
  printf '%s\n' '` + imageID + `'
  exit 0
fi
if [ "$1" = image ] && [ "$2" = save ] && [ "$3" = '` + imageID + `' ]; then
  printf docker-save-archive
  exit 0
fi
exit 64
`
	if err := os.WriteFile(docker, []byte(dockerScript), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dockerDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))
	credentials := filepath.Join(t.TempDir(), "credentials.json")
	t.Setenv("CANTELOP_CONFIG", credentials)

	requests := 0
	var apiDigest, sessionRuntimeDigest string
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		status := http.StatusOK
		headers := make(http.Header)
		response := ""
		switch requests {
		case 1:
			if request.Method != http.MethodPost || request.URL.Path != "/v1/apps/"+appID+"/deployments/uploads" ||
				request.Header.Get("Authorization") != "Bearer tenant-jwt" || !strings.HasPrefix(request.Header.Get("Idempotency-Key"), "cli-deploy:") {
				t.Fatalf("begin request = %s %s %v", request.Method, request.URL.Path, request.Header)
			}
			var body struct {
				API struct {
					Digest string `json:"digest"`
					Size   int    `json:"size_bytes"`
				} `json:"api"`
				SessionRuntime struct {
					Digest    string `json:"digest"`
					Size      int64  `json:"size_bytes"`
					MediaType string `json:"media_type"`
				} `json:"session_runtime"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body.API.Size != len(module) ||
				body.SessionRuntime.Size < 1 || body.SessionRuntime.MediaType != "application/vnd.cantelop.session-runtime.image.v1.tar+gzip" {
				t.Fatalf("bundle manifest = %#v, %v", body, err)
			}
			apiDigest, sessionRuntimeDigest = body.API.Digest, body.SessionRuntime.Digest
			status = http.StatusCreated
			response = fmt.Sprintf(`{"id":%q,"app_id":%q,"status":"uploading","expires_at":"2026-08-12T13:00:00Z","api":{"method":"PUT","url":%q,"headers":{"content-type":"application/javascript+module"}},"session_runtime":{"part_size_bytes":5242880,"parts":[{"part_number":1,"method":"PUT","url":%q,"headers":{}}]}}`,
				uploadID, appID, origin+"/r2/api", origin+"/r2/session-runtime/1")
		case 2:
			contents, _ := io.ReadAll(request.Body)
			if request.Method != http.MethodPut || request.URL.Path != "/r2/api" || !bytes.Equal(contents, module) ||
				request.Header.Get("Authorization") != "" || request.Header.Get("Content-Type") != "application/javascript+module" {
				t.Fatalf("API upload = %s %s %v %q", request.Method, request.URL.Path, request.Header, contents)
			}
		case 3:
			compressed, err := gzip.NewReader(request.Body)
			if err != nil {
				t.Fatalf("sessionRuntime gzip = %v", err)
			}
			archive, _ := io.ReadAll(compressed)
			if request.Method != http.MethodPut || request.URL.Path != "/r2/session-runtime/1" || string(archive) != "docker-save-archive" {
				t.Fatalf("Session runtime upload = %s %s %q", request.Method, request.URL.Path, archive)
			}
			headers.Set("ETag", `"part-1"`)
		case 4:
			if request.Method != http.MethodPost || request.URL.Path != "/v1/apps/"+appID+"/deployments/uploads/"+uploadID+"/complete" {
				t.Fatalf("complete request = %s %s", request.Method, request.URL.Path)
			}
			var body map[string][]controlapi.CompletedSessionRuntimePart
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil || len(body["session_runtime_parts"]) != 1 || body["session_runtime_parts"][0].ETag != `"part-1"` {
				t.Fatalf("completion = %#v, %v", body, err)
			}
			status = http.StatusAccepted
			response = fmt.Sprintf(`{"id":%q,"app_id":%q,"version":4,"status":"active","api_artifact_digest":%q,"session_runtime_artifact_digest":%q,"components":{"api":{"status":"succeeded"},"session_runtime":{"status":"succeeded"}},"created_at":"2026-08-12T12:00:00Z","updated_at":"2026-08-12T12:00:00Z"}`,
				releaseID, appID, apiDigest, sessionRuntimeDigest)
		default:
			t.Fatalf("unexpected request %s %s", request.Method, request.URL.Path)
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(response)), Header: headers}, nil
	})}
	session := &controlapi.Session{
		Client: client, Credentials: controlapi.Credentials{ControlURL: origin},
		Token: controlapi.TokenResponse{AccessToken: "tenant-jwt"}, ExpiresAt: time.Now().Add(time.Hour),
	}
	var output bytes.Buffer
	if err := runAppDeploy(context.Background(), []string{appID, "-image", "hello:latest", "-artifact", directory}, &output, session); err != nil {
		t.Fatal(err)
	}
	if requests != 4 || !strings.Contains(output.String(), "Activated deployment v4") {
		t.Fatalf("requests=%d output=%q", requests, output.String())
	}
	entries, err := os.ReadDir(filepath.Join(filepath.Dir(credentials), "deployment-retries"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("completed deployment retry state = %#v, %v", entries, err)
	}
}

func TestAppCommandsRequireValidCreationAndUnifiedDeploymentInputs(t *testing.T) {
	session := new(controlapi.Session)
	if err := runAppDeploy(context.Background(), []string{"app_0123456789abcdef0123456789abcdef", "-image", "hello"}, io.Discard, session); err == nil {
		t.Fatal("app deploy accepted no API artifact")
	}
	if err := runAppCreate(context.Background(), []string{"-slug", "hello--bad"}, io.Discard, session); err == nil {
		t.Fatal("app create accepted an ambiguous routing slug")
	}
	if err := runAppCreate(context.Background(), []string{"-slug", "hello", "-runtime-ttl", "2m"}, io.Discard, session); err == nil {
		t.Fatal("app create accepted the removed App-level runtime TTL")
	}
	if err := runAppCreate(context.Background(), []string{"-slug", "hello", "-port", "8080"}, io.Discard, session); err == nil {
		t.Fatal("app create accepted the platform-owned internal port")
	}
}

func TestConfigCommandsUseScopedPathsAndKeepSecretsOffOutput(t *testing.T) {
	const (
		origin = "http://127.0.0.1:12345"
		appID  = "app_0123456789abcdef0123456789abcdef"
	)
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		var document []byte
		if request.Body != nil {
			document, _ = io.ReadAll(request.Body)
		}
		status, response := http.StatusOK, ""
		switch requests {
		case 1:
			if request.Method != http.MethodPut || request.URL.Path != "/v1/apps/"+appID+"/config/secrets/API_TOKEN" ||
				!strings.Contains(string(document), `"value":"super-secret"`) {
				t.Fatalf("secret request = %s %s %s", request.Method, request.URL.Path, document)
			}
			response = `{"name":"API_TOKEN","kind":"secret"}`
		case 2:
			if request.Method != http.MethodGet || request.URL.Path != "/v1/apps/"+appID+"/config" {
				t.Fatalf("list request = %s %s", request.Method, request.URL.Path)
			}
			response = `{"items":[{"name":"LOG_LEVEL","kind":"variable","value":"debug"},{"name":"API_TOKEN","kind":"secret"}]}`
		case 3:
			if request.Method != http.MethodDelete || request.URL.Path != "/v1/apps/"+appID+"/config/variables/LOG_LEVEL" {
				t.Fatalf("unset request = %s %s", request.Method, request.URL.Path)
			}
			status = http.StatusNoContent
		default:
			t.Fatalf("unexpected request %s %s", request.Method, request.URL.Path)
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(response)), Header: make(http.Header)}, nil
	})}
	session := &controlapi.Session{
		Client: client, Credentials: controlapi.Credentials{ControlURL: origin},
		Token: controlapi.TokenResponse{AccessToken: "tenant-jwt"}, ExpiresAt: time.Now().Add(time.Hour),
	}
	var output bytes.Buffer
	if err := runConfigCommand(context.Background(), []string{"secret", "set", appID, "API_TOKEN"}, strings.NewReader("super-secret\n"), &output, session); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "super-secret") {
		t.Fatalf("secret output leaked value: %q", output.String())
	}
	output.Reset()
	if err := runConfigCommand(context.Background(), []string{"env", "list", appID}, nil, &output, session); err != nil {
		t.Fatal(err)
	}
	if output.String() != "LOG_LEVEL=debug\n" {
		t.Fatalf("variable list = %q", output.String())
	}
	output.Reset()
	if err := runConfigCommand(context.Background(), []string{"env", "unset", appID, "LOG_LEVEL"}, nil, &output, session); err != nil {
		t.Fatal(err)
	}
	if requests != 3 {
		t.Fatalf("requests = %d", requests)
	}
}

func TestAppCreateFailureStopsAfterTheCreationRequest(t *testing.T) {
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{
			StatusCode: http.StatusConflict,
			Body:       io.NopCloser(strings.NewReader(`{"error":"resource_conflict"}`)),
			Header:     make(http.Header),
		}, nil
	})}
	session := &controlapi.Session{
		Client:      client,
		Credentials: controlapi.Credentials{ControlURL: "http://127.0.0.1:12345"},
		Token:       controlapi.TokenResponse{AccessToken: "tenant-jwt"},
		ExpiresAt:   time.Now().Add(time.Minute),
	}
	if err := runAppCreate(context.Background(), []string{"-slug", "hello"}, io.Discard, session); err == nil {
		t.Fatal("runAppCreate() accepted failed App creation")
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want only App creation", requests)
	}
}
