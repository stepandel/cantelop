package main

import (
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

	"github.com/stepandel/cantelop/clients/cli/internal/integration"
)

var (
	credentialsTestToken    = strings.Repeat("c", 64)
	credentialsTestOldToken = strings.Repeat("a", 64)
)

func credentialsTestDocument(token, rotation, previous string) string {
	return fmt.Sprintf(`{"app_id":%q,"slug":"vera","edge_url":"https://vera.cantelop.dev","token":%q,"previous_token_expires_at":%s,"rotation":%s}`,
		operationalTestAppID, token, previous, rotation)
}

func credentialsTestStatus(t *testing.T) *bytes.Buffer {
	t.Helper()
	var status bytes.Buffer
	previousOutput, previousInterval := credentialStatusOutput, rotationPollInterval
	credentialStatusOutput, rotationPollInterval = &status, time.Millisecond
	t.Cleanup(func() { credentialStatusOutput, rotationPollInterval = previousOutput, previousInterval })
	return &status
}

func TestAppCredentialsPrintsTheProjectAppInEachFormat(t *testing.T) {
	project, credentials := operationalTestProject(t)
	t.Setenv("CANTELOP_CONFIG", credentials)
	credentialsTestStatus(t)
	for format, want := range map[string]string{
		"":      "CANTELOP_APP_SLUG=vera\nCANTELOP_INTEGRATION_TOKEN=" + credentialsTestToken + "\n",
		"token": credentialsTestToken + "\n",
	} {
		requests := 0
		client := operationalTestClient(t, &requests, func(request *http.Request) string {
			if request.Method != http.MethodGet || request.URL.Path != "/v1/apps/"+operationalTestAppID+"/integration" {
				t.Fatalf("credentials request = %s %s", request.Method, request.URL.Path)
			}
			return credentialsTestDocument(credentialsTestToken, "null", "null")
		})
		args := []string{"app", "credentials", "--config", project}
		if format != "" {
			args = append(args, "--format", format)
		}
		var output bytes.Buffer
		if err := runCustomer(context.Background(), args, &output, client); err != nil {
			t.Fatal(err)
		}
		if output.String() != want || requests != 3 {
			t.Fatalf("format %q: output %q after %d requests", format, output.String(), requests)
		}
	}

	requests := 0
	client := operationalTestClient(t, &requests, func(*http.Request) string {
		return strings.Replace(credentialsTestDocument(credentialsTestToken, "null", "null"), "https://vera.cantelop.dev", "https://edge.example", 1)
	})
	var output bytes.Buffer
	if err := runCustomer(context.Background(), []string{"app", "credentials", "--app", "vera", "--format", "app-config"}, &output, client); err != nil {
		t.Fatal(err)
	}
	var document struct {
		SchemaVersion int    `json:"schemaVersion"`
		ActiveProfile string `json:"activeProfile"`
		Profiles      map[string]struct {
			DefaultApp map[string]string   `json:"defaultApp"`
			Apps       []map[string]string `json:"apps"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(output.Bytes(), &document); err != nil || bytes.Count(output.Bytes(), []byte("\n")) != 1 {
		t.Fatalf("app-config output %q: %v", output.String(), err)
	}
	app := document.Profiles["default"].Apps
	if document.SchemaVersion != 1 || document.ActiveProfile != "default" || document.Profiles["default"].DefaultApp["slug"] != "vera" || len(app) != 1 ||
		app[0]["id"] != operationalTestAppID || app[0]["edgeUrl"] != "https://edge.example" || app[0]["accessToken"] != credentialsTestToken {
		t.Fatalf("app-config = %+v", document)
	}

	requests = 0
	client = operationalTestClient(t, &requests, func(*http.Request) string {
		return strings.Replace(credentialsTestDocument(credentialsTestToken, "null", "null"), "https://vera.cantelop.dev", "https://edge.example", 1)
	})
	output.Reset()
	if err := runCustomer(context.Background(), []string{"app", "credentials", "--app", "vera"}, &output, client); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(output.String(), "CANTELOP_EDGE_URL=https://edge.example\n") {
		t.Fatalf("custom Edge URL output = %q", output.String())
	}
}

func TestAppCredentialsReportsAppsWithoutCredentialsAndInvalidResponses(t *testing.T) {
	_, credentials := operationalTestProject(t)
	t.Setenv("CANTELOP_CONFIG", credentials)
	credentialsTestStatus(t)
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		status, body := http.StatusOK, ""
		switch requests {
		case 1:
			body = `{"access_token":"tenant-jwt","token_type":"Bearer","expires_in":60,"control_url":"` + operationalTestOrigin + `"}`
		case 2:
			body = `{"id":"` + operationalTestAppID + `","slug":"vera"}`
		default:
			status, body = http.StatusNotFound, `{"error":"integration_not_configured"}`
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	var output bytes.Buffer
	err := runCustomer(context.Background(), []string{"app", "credentials", "--app", "vera"}, &output, client)
	if err == nil || !strings.Contains(err.Error(), "after its first cantelop deploy") || output.Len() != 0 {
		t.Fatalf("missing credentials: %v, output %q", err, output.String())
	}

	for _, document := range []string{
		strings.Replace(credentialsTestDocument(credentialsTestToken, "null", "null"), operationalTestAppID, "app_"+strings.Repeat("f", 32), 1),
		credentialsTestDocument("short", "null", "null"),
		strings.Replace(credentialsTestDocument(credentialsTestToken, "null", "null"), "https://vera.cantelop.dev", "http://vera.cantelop.dev", 1),
		credentialsTestDocument(credentialsTestToken, `{"revision":2,"rotated_at":"2026-10-13T12:00:00Z","status":"done"}`, "null"),
	} {
		requests := 0
		client := operationalTestClient(t, &requests, func(*http.Request) string { return document })
		output.Reset()
		if err := runCustomer(context.Background(), []string{"app", "credentials", "--app", "vera"}, &output, client); err == nil || output.Len() != 0 {
			t.Fatalf("accepted %s: output %q", document, output.String())
		}
	}
}

func TestAppCredentialsRotateWaitsForTheEdgeAndUpdatesTheLocalProfile(t *testing.T) {
	_, credentials := operationalTestProject(t)
	t.Setenv("CANTELOP_CONFIG", credentials)
	profiles := filepath.Join(t.TempDir(), "integration.json")
	t.Setenv("CANTELOP_INTEGRATION_CONFIG", profiles)
	if err := integration.SaveApp("local", integration.AppConfiguration{ID: "app_local", Slug: "vera", EdgeURL: "http://127.0.0.1:8787", AccessToken: "local-token", RuntimeID: "vera.v1"}); err != nil {
		t.Fatal(err)
	}
	if err := integration.SaveApp("default", integration.AppConfiguration{ID: operationalTestAppID, Slug: "vera", EdgeURL: "https://vera.cantelop.dev", AccessToken: credentialsTestOldToken, RuntimeID: "vera.v1"}); err != nil {
		t.Fatal(err)
	}
	status := credentialsTestStatus(t)
	expires := `"2026-10-13T13:00:00Z"`
	rotation := func(state string) string {
		return `{"revision":5,"rotated_at":"2026-10-13T12:00:00Z","status":"` + state + `"}`
	}
	requests, polls := 0, 0
	client := operationalTestClient(t, &requests, func(request *http.Request) string {
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/v1/apps/"+operationalTestAppID+"/integration/rotate":
			body, _ := io.ReadAll(request.Body)
			if string(body) != `{"grace_seconds":3600}` || request.Header.Get("Content-Type") != "application/json" {
				t.Fatalf("rotation body = %s", body)
			}
			return credentialsTestDocument(credentialsTestToken, rotation("pending"), expires)
		case request.Method == http.MethodGet && request.URL.Path == "/v1/apps/"+operationalTestAppID+"/integration":
			polls++
			if polls < 2 {
				return credentialsTestDocument(credentialsTestToken, rotation("pending"), expires)
			}
			return credentialsTestDocument(credentialsTestToken, rotation("applied"), expires)
		}
		t.Fatalf("unexpected request %s %s", request.Method, request.URL.Path)
		return ""
	})
	var output bytes.Buffer
	if err := runCustomer(context.Background(), []string{"app", "credentials", "rotate", "--app", "vera", "--grace", "1h"}, &output, client); err != nil {
		t.Fatal(err)
	}
	if output.String() != "CANTELOP_APP_SLUG=vera\nCANTELOP_INTEGRATION_TOKEN="+credentialsTestToken+"\n" || polls != 2 {
		t.Fatalf("output %q after %d polls", output.String(), polls)
	}
	for _, message := range []string{"keeps working until 2026-10-13T13:00:00Z", "live on the Edge", "Updated the local default SDK profile"} {
		if !strings.Contains(status.String(), message) {
			t.Fatalf("status %q lacks %q", status.String(), message)
		}
	}
	document, err := os.ReadFile(profiles)
	if err != nil {
		t.Fatal(err)
	}
	var config integration.Configuration
	if err := json.Unmarshal(document, &config); err != nil {
		t.Fatal(err)
	}
	if app := config.Profiles["default"].Apps[0]; app.AccessToken != credentialsTestToken || app.RuntimeID != "vera.v1" {
		t.Fatalf("default profile = %+v", app)
	}
	if app := config.Profiles["local"].Apps[0]; app.AccessToken != "local-token" {
		t.Fatalf("local profile changed: %+v", app)
	}
	if config.ActiveProfile != "default" {
		t.Fatalf("active profile = %q", config.ActiveProfile)
	}
}

func TestAppCredentialsRotateNowWithoutWaitingAndWithoutAProfile(t *testing.T) {
	_, credentials := operationalTestProject(t)
	t.Setenv("CANTELOP_CONFIG", credentials)
	profiles := filepath.Join(t.TempDir(), "integration.json")
	t.Setenv("CANTELOP_INTEGRATION_CONFIG", profiles)
	status := credentialsTestStatus(t)
	requests := 0
	client := operationalTestClient(t, &requests, func(request *http.Request) string {
		body, _ := io.ReadAll(request.Body)
		if request.URL.Path != "/v1/apps/"+operationalTestAppID+"/integration/rotate" || string(body) != `{"grace_seconds":0}` {
			t.Fatalf("unexpected request %s %s %s", request.Method, request.URL.Path, body)
		}
		return credentialsTestDocument(credentialsTestToken, `{"revision":5,"rotated_at":"2026-10-13T12:00:00Z","status":"pending"}`, "null")
	})
	var output bytes.Buffer
	if err := runCustomer(context.Background(), []string{"app", "credentials", "rotate", "--app", "vera", "--now", "--no-wait", "--format", "token"}, &output, client); err != nil {
		t.Fatal(err)
	}
	if output.String() != credentialsTestToken+"\n" || requests != 3 || !strings.Contains(status.String(), "stops working once the Edge has the new one") {
		t.Fatalf("output %q, status %q, requests %d", output.String(), status.String(), requests)
	}
	if _, err := os.Stat(profiles); !os.IsNotExist(err) {
		t.Fatalf("rotation created an SDK profile: %v", err)
	}
}

func TestAppCredentialsRotateReportsFailedPropagationAndUnsupportedReleases(t *testing.T) {
	_, credentials := operationalTestProject(t)
	t.Setenv("CANTELOP_CONFIG", credentials)
	profiles := filepath.Join(t.TempDir(), "integration.json")
	t.Setenv("CANTELOP_INTEGRATION_CONFIG", profiles)
	if err := integration.SaveApp("default", integration.AppConfiguration{ID: operationalTestAppID, Slug: "vera", AccessToken: credentialsTestOldToken}); err != nil {
		t.Fatal(err)
	}
	credentialsTestStatus(t)
	requests := 0
	client := operationalTestClient(t, &requests, func(request *http.Request) string {
		state := "pending"
		if request.Method == http.MethodGet {
			state = "failed"
		}
		return credentialsTestDocument(credentialsTestToken, `{"revision":5,"rotated_at":"2026-10-13T12:00:00Z","status":"`+state+`"}`, `"2026-10-14T12:00:00Z"`)
	})
	var output bytes.Buffer
	err := runCustomer(context.Background(), []string{"app", "credentials", "rotate", "--app", "vera"}, &output, client)
	if err == nil || !strings.Contains(err.Error(), "--retry") || output.Len() != 0 {
		t.Fatalf("failed propagation: %v, output %q", err, output.String())
	}
	document, _ := os.ReadFile(profiles)
	if !strings.Contains(string(document), credentialsTestOldToken) {
		t.Fatal("failed rotation replaced the local token")
	}

	requests = 0
	client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		status, body := http.StatusOK, ""
		switch requests {
		case 1:
			body = `{"access_token":"tenant-jwt","token_type":"Bearer","expires_in":60,"control_url":"` + operationalTestOrigin + `"}`
		case 2:
			body = `{"id":"` + operationalTestAppID + `","slug":"vera"}`
		default:
			status, body = http.StatusConflict, `{"error":"integration_rotation_unsupported"}`
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	err = runCustomer(context.Background(), []string{"app", "credentials", "rotate", "--app", "vera"}, &output, client)
	if err == nil || !strings.Contains(err.Error(), "newer @cantelop/sdk") || !strings.Contains(err.Error(), "--now") {
		t.Fatalf("unsupported release: %v", err)
	}
}

func TestAppCredentialsRejectsInvalidArguments(t *testing.T) {
	for _, args := range [][]string{
		{"--grace", "1h"},
		{"--now"},
		{"--no-wait"},
		{"rotate", "--grace", "1h", "--now"},
		{"rotate", "--grace", "0s"},
		{"rotate", "--grace", "169h"},
		{"rotate", "--grace", "1500ms"},
		{"--format", "yaml"},
		{"--app", "Not A Slug"},
		{"extra"},
		{"rotate", "extra"},
		{"--retry"},
		{"rotate", "--retry", "--now"},
		{"rotate", "--retry", "--grace", "1h"},
	} {
		if _, err := parseAppCredentialsOptions(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	options, err := parseAppCredentialsOptions([]string{"rotate"})
	if err != nil || !options.rotate || options.grace != 24*time.Hour || !options.wait || options.format != "dotenv" {
		t.Fatalf("defaults = %+v, %v", options, err)
	}
	if err := unknownCommand([]string{"app", "credentials"}); err != nil {
		t.Fatal(err)
	}
}

func TestRotationTimeoutAndNoWaitPreserveTheLocalProfile(t *testing.T) {
	for _, noWait := range []bool{false, true} {
		t.Run(fmt.Sprintf("noWait=%t", noWait), func(t *testing.T) {
			_, credentials := operationalTestProject(t)
			t.Setenv("CANTELOP_CONFIG", credentials)
			profiles := filepath.Join(t.TempDir(), "integration.json")
			t.Setenv("CANTELOP_INTEGRATION_CONFIG", profiles)
			if err := integration.SaveApp("default", integration.AppConfiguration{ID: operationalTestAppID, Slug: "vera", AccessToken: credentialsTestOldToken}); err != nil {
				t.Fatal(err)
			}
			credentialsTestStatus(t)
			oldTimeout := rotationWaitTimeout
			rotationWaitTimeout = 0
			t.Cleanup(func() { rotationWaitTimeout = oldTimeout })
			requests := 0
			client := operationalTestClient(t, &requests, func(*http.Request) string {
				return credentialsTestDocument(credentialsTestToken, `{"revision":5,"rotated_at":"2026-10-13T12:00:00Z","status":"pending"}`, `"2026-10-14T12:00:00Z"`)
			})
			args := []string{"app", "credentials", "rotate", "--app", "vera"}
			if noWait {
				args = append(args, "--no-wait")
			}
			var output bytes.Buffer
			err := runCustomer(context.Background(), args, &output, client)
			if noWait && err != nil {
				t.Fatal(err)
			}
			if !noWait && (err == nil || !strings.Contains(err.Error(), "timed out") || output.Len() != 0) {
				t.Fatalf("timeout: %v", err)
			}
			document, _ := os.ReadFile(profiles)
			if !strings.Contains(string(document), credentialsTestOldToken) {
				t.Fatal("pending rotation replaced local profile")
			}
		})
	}
}

func TestRotationRetryResendsExistingTokenAndUpdatesProfileAfterEdgeAccepts(t *testing.T) {
	_, credentials := operationalTestProject(t)
	t.Setenv("CANTELOP_CONFIG", credentials)
	profiles := filepath.Join(t.TempDir(), "integration.json")
	t.Setenv("CANTELOP_INTEGRATION_CONFIG", profiles)
	if err := integration.SaveApp("default", integration.AppConfiguration{ID: operationalTestAppID, Slug: "vera", AccessToken: credentialsTestOldToken}); err != nil {
		t.Fatal(err)
	}
	status := credentialsTestStatus(t)
	requests := 0
	client := operationalTestClient(t, &requests, func(request *http.Request) string {
		state := "applied"
		if request.Method == http.MethodPost {
			body, _ := io.ReadAll(request.Body)
			if string(body) != `{"retry":true}` {
				t.Fatalf("retry body = %s", body)
			}
			state = "pending"
		}
		// Retrying an immediate rotation has no previous-token expiry.
		return credentialsTestDocument(credentialsTestToken, `{"revision":6,"rotated_at":"2026-10-13T12:00:00Z","status":"`+state+`"}`, "null")
	})
	var output bytes.Buffer
	if err := runCustomer(context.Background(), []string{"app", "credentials", "rotate", "--app", "vera", "--retry"}, &output, client); err != nil {
		t.Fatal(err)
	}
	document, _ := os.ReadFile(profiles)
	if !strings.Contains(string(document), credentialsTestToken) {
		t.Fatal("applied retry did not update local profile")
	}
	if !strings.Contains(status.String(), "token and grace period are unchanged") {
		t.Fatal("retry status missing")
	}
}
