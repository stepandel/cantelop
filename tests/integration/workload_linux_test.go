package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/config"
	"github.com/stepandel/cantelop/fire-fuse/guest-runtime/guestconfig"
	"github.com/stepandel/cantelop/fire-fuse/host/preflight"
	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
	"github.com/stepandel/cantelop/fire-fuse/runtime/supervisor"
)

func TestDockerWorkloadContract(t *testing.T) {
	h := newIntegrationEnvironment(t)
	if h.Config.Workspace == nil {
		t.Skip("Docker workload contract qualification requires workspace host configuration")
	}
	if h.Config.Limits.MaxTotalMemoryMiB < h.MemoryMiB {
		t.Fatalf(
			"Docker workload contract test requires limits.max_total_memory_mib >= %d",
			h.MemoryMiB,
		)
	}

	const (
		argumentValue    = "argument-from-mmds"
		environmentValue = "environment-from-mmds"
		workingDirectory = "/tmp"
		resultName       = ".fire-fuse-workload-contract"
		workloadUID      = 12000
		workloadGID      = 13000
	)
	image, err := h.Config.GetImage(h.BuildID)
	if err != nil {
		t.Fatalf("select integration image: %v", err)
	}
	image.Process = guestconfig.Process{
		Args: []string{
			"/bin/sh",
			"-c",
			workloadContractScript,
			"fire-fuse-workload-contract",
			argumentValue,
			resultName,
		},
		Env: []string{
			"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
			"FIRE_FUSE_CONTRACT_ENV=" + environmentValue,
		},
		WorkingDirectory: workingDirectory,
		UID:              workloadUID,
		GID:              workloadGID,
	}
	imageID, err := config.ContainerImageID(image)
	if err != nil {
		t.Fatalf("derive workload-contract image identity: %v", err)
	}
	h.Config.Images = map[string]config.ContainerImage{imageID: image}
	h.BuildID = imageID
	h.Config.ImageCatalog = writeWorkloadImageCatalog(t, h, imageID, image)
	if err := h.Config.Validate(); err != nil {
		t.Fatalf("validate workload-contract integration configuration: %v", err)
	}
	if err := preflight.Check(h.Config); err != nil {
		t.Fatalf("integration host prerequisites failed: %v", err)
	}

	workspaceID := fmt.Sprintf("workload-contract-%d", time.Now().UnixNano())
	workspacePath := filepath.Join(h.Config.Workspace.Root, "wsp_"+workspaceID)
	if err := os.Mkdir(workspacePath, 0o700); err != nil {
		t.Fatalf("create workload-contract workspace: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(workspacePath); err != nil {
			t.Errorf("remove workload-contract workspace: %v", err)
		}
	})
	if err := os.Chown(workspacePath, workloadUID, workloadGID); err != nil {
		t.Fatalf("assign workload-contract workspace identity: %v", err)
	}

	lifecycle, store := newWorkloadLifecycle(t, h)
	machine := startWorkspaceVM(t, h, lifecycle, store, workspaceID)

	resultPath := filepath.Join(workspacePath, resultName)
	result := waitForWorkloadContract(t, h, resultPath)
	want := strings.Join([]string{
		"rootfs=fire-fuse-container-rootfs-v1",
		"argument=" + argumentValue,
		"environment=" + environmentValue,
		"working_directory=" + workingDirectory,
		fmt.Sprintf("uid=%d", workloadUID),
		fmt.Sprintf("gid=%d", workloadGID),
		"complete=1",
		"",
	}, "\n")
	if result != want {
		t.Fatalf("guest workload contract result = %q, want %q", result, want)
	}

	terminateWorkspaceVM(t, h, lifecycle, machine.ID)
}

func writeWorkloadImageCatalog(
	t *testing.T,
	h integrationEnvironment,
	imageID string,
	image config.ContainerImage,
) string {
	t.Helper()
	contents, err := json.Marshal(config.ImageCatalogDocument{
		SchemaVersion: config.ImageCatalogSchemaVersion,
		Images:        map[string]config.ContainerImage{imageID: image},
	})
	if err != nil {
		t.Fatalf("encode workload-contract image catalog: %v", err)
	}
	path := filepath.Join(h.Config.RuntimeDir, "workload-image-catalog.json")
	if err := os.WriteFile(path, contents, 0o640); err != nil {
		t.Fatalf("write workload-contract image catalog: %v", err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatalf("protect workload-contract image catalog: %v", err)
	}
	return path
}

const workloadContractScript = `set -eu
argument=$1
result_name=$2
IFS= read -r rootfs_id < /etc/fire-fuse-container-rootfs-id
uid=
gid=
while read -r key real _; do
  case "$key" in
    Uid:) uid=$real ;;
    Gid:) gid=$real ;;
  esac
done < /proc/self/status
{
  printf 'rootfs=%s\n' "$rootfs_id"
  printf 'argument=%s\n' "$argument"
  printf 'environment=%s\n' "$FIRE_FUSE_CONTRACT_ENV"
  printf 'working_directory=%s\n' "$PWD"
  printf 'uid=%s\n' "$uid"
  printf 'gid=%s\n' "$gid"
  printf 'complete=1\n'
} > "/workspace/$result_name"
exec /bin/sleep 3600
`

func newWorkloadLifecycle(t *testing.T, h integrationEnvironment) (*microvm.Service, microvm.Store) {
	t.Helper()
	store, err := microvm.OpenSQLiteStore(h.Config.StateDB)
	if err != nil {
		t.Fatalf("open isolated workload-contract store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close isolated workload-contract store: %v", err)
		}
	})

	runtimeSupervisor, err := supervisor.New(h.Config, os.Stderr)
	if err != nil {
		t.Fatalf("construct production supervisor: %v", err)
	}
	if err := runtimeSupervisor.CheckForOrphans(nil); err != nil {
		t.Fatalf("integration host must start without fire-fuse orphans: %v", err)
	}
	lifecycle, err := microvm.NewServiceWithSupervisorAndLimits(
		store,
		runtimeSupervisor,
		microvm.AdmissionLimits{
			MaxVMs:              h.Config.Limits.MaxVMs,
			MaxTotalMemoryMiB:   h.Config.Limits.MaxTotalMemoryMiB,
			MaxConcurrentStarts: h.Config.Limits.MaxConcurrentStarts,
		},
	)
	if err != nil {
		t.Fatalf("construct production lifecycle service: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), workspaceOperationTimeout(h))
		defer cancel()
		if err := lifecycle.TerminateAll(ctx); err != nil {
			t.Errorf("emergency workload-contract cleanup: %v", err)
		}
	})
	return lifecycle, store
}

func waitForWorkloadContract(t *testing.T, h integrationEnvironment, path string) string {
	t.Helper()
	deadline := time.Now().Add(time.Duration(h.Config.Timeouts.Startup))
	var lastErr error
	var lastDocument string
	for time.Now().Before(deadline) {
		document, err := os.ReadFile(path)
		if err == nil && strings.HasSuffix(string(document), "complete=1\n") {
			return string(document)
		}
		if err != nil {
			lastErr = err
		} else {
			lastDocument = string(document)
			lastErr = fmt.Errorf("workload contract is incomplete")
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf(
		"guest workload did not publish its runtime contract to %s: %v; last document %q",
		path,
		lastErr,
		lastDocument,
	)
	return ""
}
