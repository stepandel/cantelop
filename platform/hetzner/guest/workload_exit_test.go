package guest_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWorkloadExitEvidence(t *testing.T) {
	for _, tc := range []struct{ name, result, code, status, counter, cause string }{
		{"sdk death", "signal", "killed", "KILL", "oom_kill 0\n", "workload_exit"},
		{"confirmed guest OOM", "oom-kill", "killed", "KILL", "oom_kill 1\n", "oom_confirmed"},
		{"child OOM with surviving SDK", "success", "exited", "0", "oom_kill 1\n", "oom_confirmed"},
		{"unexpected exit", "exit-code", "exited", "23", "", "workload_exit"},
		{"sanitize", "secret", "secret", "secret\"", "", "workload_exit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "memory.events")
			if tc.counter != "" {
				if err := os.WriteFile(path, []byte(tc.counter), 0600); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command("sh", "fire-fuse-record-workload-exit", path)
			cmd.Env = append(os.Environ(), "SERVICE_RESULT="+tc.result, "EXIT_CODE="+tc.code, "EXIT_STATUS="+tc.status)
			data, err := cmd.Output()
			if err != nil {
				t.Fatal(err)
			}
			var record map[string]any
			if err = json.Unmarshal(data, &record); err != nil {
				t.Fatalf("%s: %v", data, err)
			}
			if record["cause"] != tc.cause {
				t.Fatalf("%s", data)
			}
			if tc.counter == "" && record["oom_kill"] != nil {
				t.Fatal("unknown counter became zero")
			}
		})
	}
}
