package deploy

import (
	"bytes"
	"context"
	"errors"
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

func TestResolveDockerImageIDRejectsMutableOrMalformedInspectOutput(t *testing.T) {
	directory := t.TempDir()
	docker := filepath.Join(directory, "docker")
	if err := os.WriteFile(docker, []byte("#!/bin/sh\nprintf 'hello:latest\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, err := resolveDockerImageID(t.Context(), "hello:latest"); err == nil {
		t.Fatal("resolveDockerImageID() accepted a mutable image reference")
	}
}

func TestPollReleaseReportsComponentProgressWithinProvisioning(t *testing.T) {
	const appID = "app_0123456789abcdef0123456789abcdef"
	const releaseID = "rel_0123456789abcdef0123456789abcdef"
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		status, apiStatus, runtimeStatus := "provisioning", "queued", "queued"
		attempts := 0
		if calls == 2 {
			apiStatus, attempts = "succeeded", 1
		}
		if calls >= 3 {
			status, apiStatus, runtimeStatus, attempts = "active", "succeeded", "succeeded", 1
		}
		response := fmt.Sprintf(`{"id":%q,"app_id":%q,"version":1,"status":%q,"api_artifact_digest":"sha256:%s","session_runtime_artifact_digest":"sha256:%s","components":{"api":{"status":%q,"attempts":%d},"session_runtime":{"status":%q}},"created_at":"2026-08-12T12:00:00Z","updated_at":"2026-08-12T12:00:00Z"}`, releaseID, appID, status, strings.Repeat("a", 64), strings.Repeat("b", 64), apiStatus, attempts, runtimeStatus)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(response)), Header: make(http.Header)}, nil
	})}
	session := &controlapi.Session{Client: client, Credentials: controlapi.Credentials{ControlURL: "http://127.0.0.1:12345"}, Token: controlapi.TokenResponse{AccessToken: "token"}, ExpiresAt: time.Now().Add(time.Hour)}
	var output bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := pollRelease(ctx, session, appID, releaseID, &output); err != nil {
		t.Fatal(err)
	}
	if strings.Count(output.String(), ": provisioning") != 2 || !strings.Contains(output.String(), "waiting for a worker") || !strings.Contains(output.String(), "API succeeded; attempts 1") {
		t.Fatalf("missing component progress: %s", output.String())
	}
}

func fastTransientRetries(t *testing.T) {
	t.Helper()
	delay, interval := controlapi.TransientRetryDelay, releasePollInterval
	controlapi.TransientRetryDelay, releasePollInterval = time.Millisecond, time.Millisecond
	t.Cleanup(func() { controlapi.TransientRetryDelay, releasePollInterval = delay, interval })
}

func TestPollReleaseRetriesTransientReadsButNotRejections(t *testing.T) {
	fastTransientRetries(t)
	const appID = "app_0123456789abcdef0123456789abcdef"
	const releaseID = "rel_0123456789abcdef0123456789abcdef"
	active := fmt.Sprintf(`{"id":%q,"app_id":%q,"version":1,"status":"active","api_artifact_digest":"sha256:%s","session_runtime_artifact_digest":"sha256:%s","components":{"api":{"status":"succeeded"},"session_runtime":{"status":"succeeded"}},"created_at":"2026-08-12T12:00:00Z","updated_at":"2026-08-12T12:00:00Z"}`, releaseID, appID, strings.Repeat("a", 64), strings.Repeat("b", 64))
	for _, test := range []struct {
		name      string
		responses []int
		wantErr   string
	}{
		{name: "transient", responses: []int{http.StatusBadGateway, 0, http.StatusOK}},
		{name: "rejected", responses: []int{http.StatusNotFound}, wantErr: "read deployment: HTTP 404"},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				status := test.responses[min(calls, len(test.responses)-1)]
				calls++
				if status == 0 {
					return nil, errors.New("connection refused")
				}
				body := `{}`
				if status == http.StatusOK {
					body = active
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})}
			session := &controlapi.Session{Client: client, Credentials: controlapi.Credentials{ControlURL: "http://127.0.0.1:12345"}, Token: controlapi.TokenResponse{AccessToken: "token"}, ExpiresAt: time.Now().Add(time.Hour)}
			var output bytes.Buffer
			err := pollRelease(context.Background(), session, appID, releaseID, &output)
			if test.wantErr == "" && (err != nil || calls != 3 || !strings.Contains(output.String(), "Activated deployment v1")) {
				t.Fatalf("err = %v after %d calls; output = %q", err, calls, output.String())
			}
			if test.wantErr != "" && (err == nil || err.Error() != test.wantErr || calls != 1) {
				t.Fatalf("err = %v after %d calls, want %q", err, calls, test.wantErr)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }
