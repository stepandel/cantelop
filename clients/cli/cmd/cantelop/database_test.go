package main

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestDatabaseMigrationInspection(t *testing.T) {
	project, credentials := operationalTestProject(t)
	t.Setenv("CANTELOP_CONFIG", credentials)
	workspace := "wsp_0123456789abcdef0123456789abcdef"
	requests := 0
	client := operationalTestClient(t, &requests, func(request *http.Request) string {
		if request.URL.Path != "/v1/apps/"+operationalTestAppID+"/workspaces/"+workspace+"/database/migrations" {
			t.Fatalf("unexpected request: %s", request.URL.Path)
		}
		return `{"workspace_id":"` + workspace + `","application":[{"digest":"sha256:abc","statements":["CREATE TABLE tasks (id TEXT)"],"applied_at":2000}],"expected_application_digest":null}`
	})
	var output bytes.Buffer
	if err := runDatabaseMigrations(context.Background(), []string{workspace, "--config", project}, &output, client); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "sha256:abc") {
		t.Fatal(output.String())
	}
	output.Reset()
	requests = 0
	if err := runDatabaseMigrations(context.Background(), []string{workspace, "--json", "--config", project}, &output, client); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "CREATE TABLE tasks") {
		t.Fatal(output.String())
	}
}
