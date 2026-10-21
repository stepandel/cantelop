package main

import (
	"bytes"
	"context"
	"encoding/json"
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

func TestProjectEnvSyncWithoutDeployment(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "cantelop.json")
	for name, content := range map[string]string{
		"cantelop.json": `{"app":"vera","api":"api.ts","session":"session.ts","environment":{"API_TOKEN":{"secret":true,"required":true},"LOG_LEVEL":{"required":true}}}`,
		"api.ts":        "export default {}", "session.ts": "export {}",
		".env": "API_TOKEN=private-token\nLOG_LEVEL=debug\nUNDECLARED=ignored\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Any attempt to build would fail: sync must not need Node or Docker.
	t.Setenv("PATH", root)
	t.Setenv("CANTELOP_CONFIG", filepath.Join(root, "missing-credentials.json"))
	var output bytes.Buffer
	if err := run(context.Background(), []string{"env", "sync", "-config", configPath, "--dry-run"}, &output, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Would set API_TOKEN (secret)") || !strings.Contains(output.String(), "Would set LOG_LEVEL (variable)") || strings.Contains(output.String(), "private-token") || strings.Contains(output.String(), "UNDECLARED") {
		t.Fatalf("dry run output: %q", output.String())
	}
	const appID = "app_0123456789abcdef0123456789abcdef"
	calls := 0
	failUpdate := false
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		status, response := 200, ""
		switch calls {
		case 1:
			if r.Method != http.MethodGet || r.URL.Path != "/v1/apps/by-slug/vera" {
				t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
			}
			response = `{"id":"` + appID + `","slug":"vera"}`
		case 2, 3:
			name, kind, value := "API_TOKEN", "secret", "private-token"
			if calls == 3 {
				name, kind, value = "LOG_LEVEL", "variable", "debug"
			}
			if r.Method != http.MethodPut || r.URL.Path != "/v1/apps/"+appID+"/config/"+kind+"s/"+name {
				t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
			}
			var payload map[string]string
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload["value"] != value {
				t.Fatalf("incorrect value for %s", name)
			}
			response = `{"name":"` + name + `","kind":"` + kind + `"}`
			if failUpdate {
				status, response = 409, `{"error":"conflict"}`
			}
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response))}, nil
	})}
	session := &controlapi.Session{Client: client, Credentials: controlapi.Credentials{ControlURL: "http://127.0.0.1:12345"}, Token: controlapi.TokenResponse{AccessToken: "token"}, ExpiresAt: time.Now().Add(time.Hour)}
	options := projectEnvSyncOptions{configPath: configPath, envFile: ".env"}
	output.Reset()
	if err := runProjectEnvSync(context.Background(), options, &output, session); err != nil {
		t.Fatal(err)
	}
	if calls != 3 || !strings.Contains(output.String(), "Updated 2 configuration values for vera") || strings.Contains(output.String(), "private-token") {
		t.Fatalf("calls=%d output=%q", calls, output.String())
	}
	calls, failUpdate = 0, true
	if err := runProjectEnvSync(context.Background(), options, io.Discard, session); err == nil || calls != 2 {
		t.Fatalf("failed sync: calls=%d err=%v", calls, err)
	}
	calls = 0
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("LOG_LEVEL=debug\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runProjectEnvSync(context.Background(), options, io.Discard, session); err == nil || calls != 0 || !strings.Contains(err.Error(), "API_TOKEN") {
		t.Fatalf("missing required variable: calls=%d err=%v", calls, err)
	}
}

func TestProjectEnvSyncOptions(t *testing.T) {
	options, err := parseProjectEnvSyncOptions(nil)
	if err != nil || options.envFile != ".env" || options.configPath != projectconfig.DefaultPath {
		t.Fatalf("options=%v err=%v", options, err)
	}
	for _, args := range [][]string{{"--env-file", ""}, {"--config", ""}, {"extra"}, {"--create-app"}} {
		if _, err := parseProjectEnvSyncOptions(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
