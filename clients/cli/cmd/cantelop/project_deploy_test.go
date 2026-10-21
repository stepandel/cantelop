package main

import (
	"archive/tar"
	"bytes"
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
	"github.com/stepandel/cantelop/clients/cli/internal/projectconfig"
)

func TestResolveProjectAppCreatesMissingAppAfterConfirmation(t *testing.T) {
	const (
		origin = "https://console.example.test"
		appID  = "app_0123456789abcdef0123456789abcdef"
	)
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		status, response := http.StatusNotFound, `{"error":"not_found"}`
		if requests == 1 {
			if request.Method != http.MethodGet || request.URL.Path != "/v1/apps/by-slug/vera" {
				t.Fatalf("resolve request = %s %s", request.Method, request.URL.Path)
			}
		} else if requests == 2 {
			if request.Method != http.MethodPost || request.URL.Path != "/v1/apps" {
				t.Fatalf("create request = %s %s", request.Method, request.URL.Path)
			}
			var input map[string]any
			if json.NewDecoder(request.Body).Decode(&input) != nil || input["slug"] != "vera" || input["workspace"] != nil {
				t.Fatalf("create input = %#v", input)
			}
			status = http.StatusCreated
			response = fmt.Sprintf(`{"id":%q,"slug":"vera"}`, appID)
		} else {
			t.Fatalf("unexpected request %s %s", request.Method, request.URL.Path)
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(response)), Header: make(http.Header)}, nil
	})}
	session := &controlapi.Session{
		Client: client, Credentials: controlapi.Credentials{ControlURL: origin},
		Token: controlapi.TokenResponse{AccessToken: "tenant-jwt"}, ExpiresAt: time.Now().Add(time.Hour),
	}
	confirmed := ""
	var output bytes.Buffer
	resolved, err := resolveProjectApp(context.Background(), session, "vera", false, func(slug string) (bool, error) {
		confirmed = slug
		return true, nil
	}, &output)
	if err != nil || resolved != appID || confirmed != "vera" || requests != 2 ||
		!strings.Contains(output.String(), "Created App vera") {
		t.Fatalf("resolved=%q confirmed=%q requests=%d output=%q err=%v", resolved, confirmed, requests, output.String(), err)
	}
}

func TestResolveProjectAppRequiresExplicitCreationWithoutPrompt(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Body:       io.NopCloser(strings.NewReader(`{"error":"not_found"}`)),
			Header:     make(http.Header),
		}, nil
	})}
	session := &controlapi.Session{
		Client: client, Credentials: controlapi.Credentials{ControlURL: "https://console.example.test"},
		Token: controlapi.TokenResponse{AccessToken: "tenant-jwt"}, ExpiresAt: time.Now().Add(time.Hour),
	}
	_, err := resolveProjectApp(context.Background(), session, "vera", false, nil, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "--create-app") {
		t.Fatalf("resolveProjectApp() error = %v", err)
	}
}

func TestConfirmAppCreationDefaultsToNo(t *testing.T) {
	for _, test := range []struct {
		answer string
		want   bool
	}{{"yes\n", true}, {"y\n", true}, {"\n", false}, {"no\n", false}, {"yes", true}, {"", false}} {
		var output bytes.Buffer
		got, err := confirmAppCreation(context.Background(), strings.NewReader(test.answer), &output, "vera")
		if err != nil || got != test.want || !strings.Contains(output.String(), `App "vera" does not exist`) {
			t.Fatalf("answer=%q got=%t output=%q err=%v", test.answer, got, output.String(), err)
		}
	}
}

func TestParseProjectDeployOptionsSupportsNonInteractiveAppCreation(t *testing.T) {
	options, err := parseProjectDeployOptions([]string{"--create-app", "-config", "project.json"})
	if err != nil || !options.createApp || options.configPath != "project.json" {
		t.Fatalf("options = %#v, err = %v", options, err)
	}
}

func TestProjectDeployBuildsAndActivatesOneCompleteRelease(t *testing.T) {
	t.Setenv("CANTELOP_CONFIG", filepath.Join(t.TempDir(), "credentials.json"))
	const (
		origin    = "http://127.0.0.1:12345"
		appID     = "app_0123456789abcdef0123456789abcdef"
		uploadID  = "upl_0123456789abcdef0123456789abcdef"
		releaseID = "rel_0123456789abcdef0123456789abcdef"
		imageID   = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		baseID    = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
		digest    = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "api.ts"), []byte("export default {}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "server.ts"), []byte("export {}"), 0o644); err != nil {
		t.Fatal(err)
	}
	config := `{"app":"vera","api":"src/api.ts","session":"src/server.ts","environment":{"API_TOKEN":{"secret":true,"required":true},"LOG_LEVEL":{"required":true}}}`
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("API_TOKEN=hidden-token\nLOG_LEVEL=debug\nUNDECLARED=ignore\n"), 0600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, projectconfig.DefaultPath)
	if err := os.WriteFile(configPath, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}

	commands := t.TempDir()
	var rootfs bytes.Buffer
	archive := tar.NewWriter(&rootfs)
	if err := archive.WriteHeader(&tar.Header{Name: "usr/lib/x86_64-linux-gnu/ld-linux-x86-64.so.2", Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(commands, "rootfs.tar"), rootfs.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	nodeScript := `#!/bin/sh
test "$1" = "--input-type=module" || exit 61
test "$4" = "` + filepath.Join(root, "src", "api.ts") + `" || exit 62
mkdir -p "$5"
printf '%s' '{"schema_version":2,"kind":"cantelop-edge-api","main_module":"worker.mjs"}' > "$5/cantelop-api.json"
printf '%s' 'export default {fetch(){return new Response("ok")}}' > "$5/worker.mjs"
test "$6" = "` + filepath.Join(root, "src", "server.ts") + `" || exit 65
mkdir -p "$7"
printf '%s' 'console.log("session-runtime")' > "$7/session-runtime.mjs"
`
	dockerScript := `#!/bin/sh
if [ "$1" = build ]; then
  test "$2" = --platform && test "$3" = linux/amd64 || exit 63
  while [ "$#" -gt 0 ]; do
    if [ "$1" = --iidfile ]; then
      shift
      case "$1" in
		*session-runtime-base-image-id) printf '%s\n' '` + baseID + `' > "$1" ;;
        *) printf '%s\n' '` + imageID + `' > "$1" ;;
      esac
      exit 0
    fi
    shift
  done
fi
if [ "$1" = image ] && [ "$2" = inspect ] && [ "$3" != "--format={{.Id}}" ]; then
  printf '%s\n' '[{"Architecture":"amd64","OS":"linux","Config":{}}]'
  exit 0
fi
if [ "$1" = create ]; then
  printf '%s\n' 'cccccccccccccccccccccccccccccccc'
  exit 0
fi
if [ "$1" = export ]; then
  cat "` + filepath.Join(commands, "rootfs.tar") + `"
  exit 0
fi
if [ "$1" = image ] && [ "$2" = inspect ]; then
  printf '%s\n' '` + imageID + `'
  exit 0
fi
if [ "$1" = image ] && [ "$2" = save ] && [ "$3" = '` + imageID + `' ]; then
  printf session-runtime-archive
  exit 0
fi
exit 64
`
	if err := os.WriteFile(filepath.Join(commands, "node"), []byte(nodeScript), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(commands, "docker"), []byte(dockerScript), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", commands+string(os.PathListSeparator)+os.Getenv("PATH"))
	var buildOutput bytes.Buffer
	if err := runProjectBuild(context.Background(), []string{"-config", configPath}, &buildOutput); err != nil {
		t.Fatalf("runProjectBuild() error = %v; output = %q", err, buildOutput.String())
	}
	if !strings.Contains(buildOutput.String(), "Build succeeded for vera") ||
		!strings.Contains(buildOutput.String(), imageID) {
		t.Fatalf("build output = %q", buildOutput.String())
	}
	var dryRunOutput bytes.Buffer
	if err := deployProject([]string{"--dry-run", "--env-file", ".env", "-config", configPath}, &dryRunOutput, nil); err != nil {
		t.Fatalf("deployProject(--dry-run) error = %v; output = %q", err, dryRunOutput.String())
	}
	if !strings.Contains(dryRunOutput.String(), "Dry run succeeded for vera") ||
		!strings.Contains(dryRunOutput.String(), "no release was uploaded") ||
		!strings.Contains(dryRunOutput.String(), "Would set API_TOKEN (secret)") || strings.Contains(dryRunOutput.String(), "hidden-token") {
		t.Fatalf("dry-run output = %q", dryRunOutput.String())
	}

	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		status := http.StatusOK
		response := ""
		switch requests {
		case 1:
			if request.Method != http.MethodGet || request.URL.Path != "/v1/apps/by-slug/vera" {
				t.Fatalf("resolve request = %s %s", request.Method, request.URL.Path)
			}
			response = fmt.Sprintf(`{"id":%q,"slug":"vera"}`, appID)
		case 2, 3:
			name, kind, value := "API_TOKEN", "secret", "hidden-token"
			if requests == 3 {
				name, kind, value = "LOG_LEVEL", "variable", "debug"
			}
			if request.Method != http.MethodPut || request.URL.Path != "/v1/apps/"+appID+"/config/"+kind+"s/"+name {
				t.Fatalf("config request: %s %s", request.Method, request.URL.Path)
			}
			var payload map[string]string
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil || payload["value"] != value {
				t.Fatalf("unexpected configuration for %s", name)
			}
			response = fmt.Sprintf(`{"name":%q,"kind":%q}`, name, kind)
		case 4:
			if request.Method != http.MethodPost || request.URL.Path != "/v1/apps/"+appID+"/deployments/uploads" {
				t.Fatalf("begin request = %s %s", request.Method, request.URL.Path)
			}
			status = http.StatusOK
			response = fmt.Sprintf(`{"id":%q,"app_id":%q,"status":"finalized","expires_at":"2026-08-12T13:00:00Z","release_id":%q}`, uploadID, appID, releaseID)
		case 5:
			if request.Method != http.MethodGet || request.URL.Path != "/v1/apps/"+appID+"/deployments/"+releaseID {
				t.Fatalf("poll request = %s %s", request.Method, request.URL.Path)
			}
			response = fmt.Sprintf(`{"id":%q,"app_id":%q,"version":7,"status":"active","api_artifact_digest":%q,"session_runtime_artifact_digest":%q,"components":{"api":{"status":"succeeded"},"session_runtime":{"status":"succeeded"}},"created_at":"2026-08-12T12:00:00Z","updated_at":"2026-08-12T12:00:00Z"}`, releaseID, appID, digest, digest)
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
	if err := deployProject([]string{"--env-file", ".env", "-config", configPath}, &output, session); err != nil {
		t.Fatalf("deployProject() error = %v; output = %q", err, output.String())
	}
	if requests != 5 || strings.Contains(output.String(), "hidden-token") || !strings.Contains(output.String(), "Building API artifact") ||
		!strings.Contains(output.String(), "Building Session runtime image for linux/amd64") ||
		!strings.Contains(output.String(), "Bundling SDK Session runtime for platform Bun") ||
		!strings.Contains(output.String(), "Activated deployment v7") {
		t.Fatalf("requests=%d output=%q", requests, output.String())
	}
}

// deployProject parses deploy arguments the way the command does, then
// deploys through an existing tenant session.
func deployProject(args []string, output io.Writer, session *controlapi.Session) error {
	options, err := parseProjectDeployOptions(args)
	if err != nil {
		return err
	}
	return runProjectDeployOptions(context.Background(), options, output, session, nil)
}
