package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/stepandel/cantelop/clients/cli/internal/deploy"
)

const cancelTestUploadID = "upl_0123456789abcdef0123456789abcdef"

func TestDeploymentCancelDiscoversUploadWithoutBuildingAndClearsOnlyItsRetryKey(t *testing.T) {
	project, credentials := operationalTestProject(t)
	t.Setenv("CANTELOP_CONFIG", credentials)
	digest := "sha256:" + strings.Repeat("a", 64)
	key, path, err := deploy.RetryIdentity(operationalTestAppID, digest, digest)
	if err != nil {
		t.Fatal(err)
	}
	_, otherPath, err := deploy.RetryIdentity(operationalTestAppID, digest, "sha256:"+strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	requests := 0
	client := operationalTestClient(t, &requests, func(request *http.Request) string {
		base := "/v1/apps/" + operationalTestAppID + "/deployments/uploads"
		if request.Method == http.MethodGet && request.URL.Path == base {
			return fmt.Sprintf(`{"items":[{"id":%q,"app_id":%q,"status":"preparing"}]}`, cancelTestUploadID, operationalTestAppID)
		}
		if request.Method != http.MethodDelete || request.URL.Path != base+"/"+cancelTestUploadID {
			t.Fatalf("cancel request = %s %s", request.Method, request.URL.Path)
		}
		return fmt.Sprintf(`{"id":%q,"app_id":%q,"status":"cancelled","idempotency_key":%q,"cleanup_pending":true}`, cancelTestUploadID, operationalTestAppID, key)
	})
	var output bytes.Buffer
	if err := runCustomer(context.Background(), []string{"deploy", "cancel", "--config", project}, &output, client); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "App upload lock released") || !strings.Contains(output.String(), "Storage cleanup is pending") {
		t.Fatalf("output = %q", output.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("cancelled retry key still present: %v", err)
	}
	if _, err := os.Stat(otherPath); err != nil {
		t.Fatalf("unrelated retry state: %v", err)
	}
	newKey, _, err := deploy.RetryIdentity(operationalTestAppID, digest, digest)
	if err != nil || newKey == key {
		t.Fatalf("new deploy reused cancelled key: %v", err)
	}
	if requests != 4 {
		t.Fatalf("requests = %d", requests)
	}
}

func TestDeploymentCancelEmptyPendingListIsNoOp(t *testing.T) {
	project, credentials := operationalTestProject(t)
	t.Setenv("CANTELOP_CONFIG", credentials)
	requests := 0
	client := operationalTestClient(t, &requests, func(request *http.Request) string {
		if request.Method != http.MethodGet {
			t.Fatalf("unexpected mutation: %s", request.Method)
		}
		return `{"items":[]}`
	})
	var output bytes.Buffer
	if err := runDeploymentCancel(context.Background(), []string{"--config", project}, &output, client); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "No pending deployment upload") {
		t.Fatal(output.String())
	}
}

func TestDeploymentCancelExplicitIDsNeedNoProjectOrListAndReportFinalizationRace(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(fmt.Sprint(conflict), func(t *testing.T) {
			_, credentials := operationalTestProject(t)
			t.Setenv("CANTELOP_CONFIG", credentials)
			requests := 0
			client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				requests++
				response := ""
				status := http.StatusOK
				if requests == 1 {
					if request.URL.Path != "/api/cli/token" {
						t.Fatal(request.URL.Path)
					}
					response = `{"access_token":"tenant-jwt","token_type":"Bearer","expires_in":60,"control_url":"` + operationalTestOrigin + `"}`
				} else {
					if request.Method != http.MethodDelete || request.URL.Path != "/v1/apps/"+operationalTestAppID+"/deployments/uploads/"+cancelTestUploadID {
						t.Fatalf("request = %s %s", request.Method, request.URL.Path)
					}
					response = fmt.Sprintf(`{"id":%q,"app_id":%q,"status":"cancelled"}`, cancelTestUploadID, operationalTestAppID)
					if conflict {
						status = http.StatusConflict
						response = `{"error":"conflict"}`
					}
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(response)), Header: make(http.Header)}, nil
			})}
			var output bytes.Buffer
			err := runDeploymentCancel(context.Background(), []string{"--app", operationalTestAppID, "--upload", cancelTestUploadID, "--config", "/missing/config"}, &output, client)
			if conflict {
				if err == nil || !strings.Contains(err.Error(), "already became a release") {
					t.Fatalf("error = %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if requests != 2 {
				t.Fatalf("requests = %d", requests)
			}
		})
	}
}

func TestDeploymentCancelRejectsForeignUploadFromDiscovery(t *testing.T) {
	project, credentials := operationalTestProject(t)
	t.Setenv("CANTELOP_CONFIG", credentials)
	requests := 0
	client := operationalTestClient(t, &requests, func(request *http.Request) string {
		if request.Method != http.MethodGet {
			t.Fatalf("unexpected mutation: %s", request.Method)
		}
		return fmt.Sprintf(`{"items":[{"id":%q,"app_id":"app_ffffffffffffffffffffffffffffffff","status":"uploading"}]}`, cancelTestUploadID)
	})
	if err := runDeploymentCancel(context.Background(), []string{"--config", project}, io.Discard, client); err == nil {
		t.Fatal("accepted foreign upload")
	}
}

func TestDeploymentCancelRejectsInvalidArguments(t *testing.T) {
	for _, args := range [][]string{{"--upload", "../escape"}, {"--app", "bad/name"}, {"extra"}, {"--dry-run"}, {"--config", ""}} {
		if err := runDeploymentCancel(context.Background(), args, io.Discard, nil); err == nil || !strings.Contains(err.Error(), "usage:") {
			t.Fatalf("args %v: %v", args, err)
		}
	}
}

func TestReleaseRecoveryCommandsReuseArtifacts(t *testing.T) {
	for _, action := range []string{"cancel", "restart"} {
		t.Run(action, func(t *testing.T) {
			project, credentials := operationalTestProject(t)
			t.Setenv("CANTELOP_CONFIG", credentials)
			requests := 0
			id := "rel_0123456789abcdef0123456789abcdef"
			client := operationalTestClient(t, &requests, func(request *http.Request) string {
				if request.Method != http.MethodPost || request.URL.Path != "/v1/apps/"+operationalTestAppID+"/deployments/"+id+"/"+action {
					t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
				}
				return fmt.Sprintf(`{"id":%q,"app_id":%q,"version":1,"status":"cancelling","api_artifact_digest":"sha256:%s","session_runtime_artifact_digest":"sha256:%s","components":{"api":{"status":"succeeded"},"session_runtime":{"status":"failed","error_code":"deployment_cancelled"}}}`, id, operationalTestAppID, strings.Repeat("a", 64), strings.Repeat("b", 64))
			})
			var output bytes.Buffer
			if err := runCustomer(context.Background(), []string{"deploy", action, "--release", id, "--config", project}, &output, client); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), "is cancelling") {
				t.Fatalf("output: %s", output.String())
			}
			if action == "restart" && !strings.Contains(output.String(), "automatically using the uploaded artifacts") {
				t.Fatalf("output: %s", output.String())
			}
			if requests != 3 {
				t.Fatalf("requests: %d", requests)
			}
		})
	}
}

func TestDeploymentRecoveryRejectsAmbiguousTargets(t *testing.T) {
	var output bytes.Buffer
	if err := runDeploymentCancel(context.Background(), []string{"--release", "rel_0123456789abcdef0123456789abcdef", "--upload", cancelTestUploadID}, &output, nil); err == nil {
		t.Fatal("accepted both targets")
	}
	if err := runDeploymentRestart(context.Background(), nil, &output, nil); err == nil {
		t.Fatal("accepted missing release")
	}
}
