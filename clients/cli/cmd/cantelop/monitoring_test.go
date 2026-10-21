package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestMonitoringCommandsUseAppScopedSources(t *testing.T) {
	trace := fmt.Sprintf(`{"app_id":%q,"requests":[{"id":"req_one","method":"POST","path":"/chat","logs":[{"body":"hello\u001b[31m"}],"messages":[{"id":"msg_one","attempts":[{"id":"attempt_one","trace_id":"abc123","spans":[{"id":"span_one","attributes":{"counter":9007199254740993}}]}]}]},{"id":"req_two"}],"unlinked_messages":[{"id":"msg_unlinked","attempts":[]}]}`, operationalTestAppID)
	snapshot := fmt.Sprintf(`{"ready":true,"observed_at":"2026-09-11T12:00:00Z","sessions":[],"workspaces":[],"sandboxes":[{"id":"sandbox_one","app_id":%q,"state":"running","vcpus":2,"memory_mib":512,"usage":{"cpu_usage_us":1234,"memory_current_bytes":1024},"failure":{"reason":"bad\u001b[31m"}}]}`, operationalTestAppID)
	for _, tc := range []struct {
		kind, id      string
		json          bool
		payload, want string
	}{
		{"traces", "", true, trace, "9007199254740993"},
		{"traces", "abc123", true, trace, "req_one"},
		{"traces", "span_one", false, trace, "counter: 9007199254740993"},
		{"traces", "msg_unlinked", true, trace, "msg_unlinked"},
		{"traces", "", false, trace, `hello\x1b[31m`},
		{"sandboxes", "", true, snapshot, "observed_at"},
		{"sandboxes", "sandbox_one", true, snapshot, "sandbox_one"},
		{"sandboxes", "sandbox_one", false, snapshot, "cpu_usage_us: 1234"},
		{"sandboxes", "", false, snapshot, "STARTUP ms"},
	} {
		t.Run(tc.kind+tc.id+fmt.Sprint(tc.json), func(t *testing.T) {
			project, credentials := operationalTestProject(t)
			t.Setenv("CANTELOP_CONFIG", credentials)
			requests := 0
			client := operationalTestClient(t, &requests, func(r *http.Request) string {
				if r.Method != http.MethodGet || r.URL.Path != "/v1/apps/"+operationalTestAppID+"/"+tc.kind || r.Header.Get("Authorization") != "Bearer tenant-jwt" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				return tc.payload
			})
			args := []string{tc.kind, "-config", project}
			if tc.id != "" {
				args = append(args, tc.id)
			}
			if tc.json {
				args = append(args, "--json")
			}
			var output bytes.Buffer
			if err := run(context.Background(), args, &output, client); err != nil {
				t.Fatal(err)
			}
			if requests != 3 || !strings.Contains(output.String(), tc.want) || strings.Contains(output.String(), "\x1b") {
				t.Fatalf("output=%q requests=%d", output.String(), requests)
			}
			if tc.json && !json.Valid(output.Bytes()) {
				t.Fatal("invalid JSON")
			}
			if tc.kind == "traces" && tc.id != "" && strings.Contains(output.String(), "req_two") {
				t.Fatal("unrelated root in filtered trace")
			}
		})
	}
}

func TestMonitoringRejectsInvalidDataAndAbsentIDs(t *testing.T) {
	for _, tc := range []struct{ kind, id, payload string }{
		{"traces", "", `null`},
		{"traces", "", `{"app_id":"other","requests":[],"unlinked_messages":[]}`},
		{"traces", "", fmt.Sprintf(`{"app_id":%q,"requests":null,"unlinked_messages":[]}`, operationalTestAppID)},
		{"traces", "missing", fmt.Sprintf(`{"app_id":%q,"requests":[],"unlinked_messages":[]}`, operationalTestAppID)},
		{"sandboxes", "", `{"ready":true,"observed_at":"now","sandboxes":[{"id":"foreign","app_id":"other"}],"sessions":[],"workspaces":[]}`},
		{"sandboxes", "missing", `{"ready":true,"observed_at":"now","sandboxes":[],"sessions":[],"workspaces":[]}`},
	} {
		project, credentials := operationalTestProject(t)
		t.Setenv("CANTELOP_CONFIG", credentials)
		requests := 0
		client := operationalTestClient(t, &requests, func(*http.Request) string { return tc.payload })
		args := []string{"-config", project}
		if tc.id != "" {
			args = append(args, tc.id)
		}
		var output bytes.Buffer
		if err := runMonitoring(context.Background(), tc.kind, args, &output, client); err == nil || output.Len() != 0 {
			t.Fatalf("accepted invalid data or absent ID: %v", err)
		}
	}
}
