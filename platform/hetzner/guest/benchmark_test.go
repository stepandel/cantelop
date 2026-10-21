package guest_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestWorkspaceBenchmarkProtocol(t *testing.T) {
	t.Parallel()

	path := filepath.Join(".", "fire-fuse-workspace-benchmark")
	if output, err := exec.Command("/bin/bash", "-n", path).CombinedOutput(); err != nil {
		t.Fatalf("bash -n: %v\n%s", err, output)
	}

	output, err := exec.Command("/bin/bash", path, "describe").Output()
	if err != nil {
		t.Fatalf("describe benchmark protocol: %v", err)
	}
	var description struct {
		SchemaVersion int      `json:"schema_version"`
		Workloads     []string `json:"workloads"`
	}
	if err := json.Unmarshal(output, &description); err != nil {
		t.Fatalf("decode benchmark description %q: %v", output, err)
	}
	if description.SchemaVersion != 2 {
		t.Errorf("schema version = %d, want 2", description.SchemaVersion)
	}
	wantWorkloads := []string{
		"small-files",
		"dependency-tree",
		"sequential-write",
		"sequential-read",
		"sequential-read-at",
		"sequential-clean",
	}
	if !reflect.DeepEqual(description.Workloads, wantWorkloads) {
		t.Errorf("workloads = %q, want %q", description.Workloads, wantWorkloads)
	}
}

func TestWorkspaceBenchmarkRejectsInvalidScheduledStart(t *testing.T) {
	t.Parallel()

	path := filepath.Join(".", "fire-fuse-workspace-benchmark")
	output, err := exec.Command(
		"/bin/bash",
		path,
		"sequential-read-at",
		"1",
		"scheduled-test",
		"invalid",
	).CombinedOutput()
	if err == nil {
		t.Fatal("sequential-read-at accepted an invalid timestamp")
	}
	if !strings.Contains(string(output), "19-digit Unix nanosecond timestamp") {
		t.Fatalf("unexpected error output: %s", output)
	}
}

func TestWorkspaceBenchmarkPinsWorkspacePaths(t *testing.T) {
	t.Parallel()

	document, err := os.ReadFile(filepath.Join(".", "fire-fuse-workspace-benchmark"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(document)
	for _, control := range []string{
		`/usr/bin/mkdir --mode=0700 -- "${path}"`,
		`cd -P -- "${run_directory}"`,
		"oflag=nofollow",
		"iflag=nofollow",
		`/usr/bin/tar --no-same-owner -xf "${archive}" -C .`,
	} {
		if !strings.Contains(script, control) {
			t.Errorf("workspace benchmark is missing path-pinning control %q", control)
		}
	}
}
