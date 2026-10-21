package integration

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/host/preflight"
	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
	"github.com/stepandel/cantelop/fire-fuse/runtime/supervisor"
)

const (
	integrationStorageOutagesEnv = "FIRE_FUSE_INTEGRATION_STORAGE_OUTAGES"
	redisService                 = "fire-fuse-redis.service"
	minioService                 = "fire-fuse-minio.service"
	juicefsGatewayService        = "fire-fuse-juicefs-gateway.service"
	nfsGatewayService            = "nfs-kernel-server.service"
	juicefsBinary                = "/usr/local/bin/juicefs"
)

func TestRedisOutageBlocksWorkspaceStartupAndRecovers(t *testing.T) {
	requireStorageOutageQualification(t)
	h := newIntegrationEnvironment(t)
	lifecycle, runtimeSupervisor, store := newWorkspaceFailureLifecycle(t, h)

	restoreRedis := stopWorkspaceBackend(t, redisService, h.Config.Workspace.Root)
	ctx, cancel := context.WithTimeout(
		context.Background(),
		time.Duration(h.Config.Timeouts.Startup)+time.Second,
	)
	workspaceID := ensureIntegrationApp(t, store, "redis-outage", "redis-outage", h.BuildID)
	failed, err := lifecycle.CreateAndStart(ctx, nextIntegrationSandboxID(), microvm.CreateSpec{
		AppID: "redis-outage", WorkspaceID: workspaceID,
	})
	cancel()
	if !errors.Is(err, microvm.ErrRuntimeUnavailable) {
		t.Fatalf("workspace startup error = %v, want ErrRuntimeUnavailable", err)
	}
	if failed.State != microvm.StateFailed {
		t.Fatalf("failed startup state = %q, want %q", failed.State, microvm.StateFailed)
	}
	if failed.Runtime != (microvm.RuntimeOwnership{}) {
		t.Fatalf("failed startup retained runtime ownership: %#v", failed.Runtime)
	}
	if runtimeSupervisor.IsRunning(failed.ID) {
		t.Fatal("Firecracker remained running after Redis-outage startup failure")
	}
	if err := runtimeSupervisor.CheckForOrphans([]microvm.Sandbox{failed}); err != nil {
		t.Fatalf("Redis-outage startup left host resources behind: %v", err)
	}

	terminateWorkspaceVM(t, h, lifecycle, failed.ID)
	if err := restoreRedis(); err != nil {
		t.Fatalf("restore %s: %v", redisService, err)
	}

	recovered := startWorkspaceVM(t, h, lifecycle, store, "redis-outage")
	terminateWorkspaceVM(t, h, lifecycle, recovered.ID)

	persisted, err := store.Get(failed.ID)
	if err != nil {
		t.Fatalf("read failed-start lifecycle record: %v", err)
	}
	if persisted.State != microvm.StateTerminated {
		t.Fatalf("cleaned failed-start state = %q, want %q", persisted.State, microvm.StateTerminated)
	}
}

func TestMinIOOutageFailsUncachedReadAndRecovers(t *testing.T) {
	requireStorageOutageQualification(t)
	h := newIntegrationEnvironment(t)
	lifecycle, _, store := newWorkspaceFailureLifecycle(t, h)
	sshKey := requiredEnvironment(t, integrationSSHKeyEnv)

	const workspaceID = "minio-outage"
	seed := startWorkspaceVM(t, h, lifecycle, store, workspaceID)
	markerPath := "/workspace/.fire-fuse-minio-outage-" + seed.ID
	marker := []byte("fire-fuse MinIO outage recovery\n")
	runSSHEventually(
		t, h, sshKey, seed.Network.GuestIP, marker,
		"dd", "of="+markerPath, "status=none", "conv=fsync",
	)
	terminateWorkspaceVM(t, h, lifecycle, seed.ID)
	hostMarkerPath := filepath.Join(
		h.Config.Workspace.Root,
		workspaceID,
		filepath.Base(markerPath),
	)
	if err := evictJuiceFSCache(hostMarkerPath); err != nil {
		t.Fatalf("evict MinIO outage marker from JuiceFS cache: %v", err)
	}

	probe := startWorkspaceVM(t, h, lifecycle, store, workspaceID)
	// Prove SSH and the marker's metadata are available before injecting the
	// outage without reading (and therefore caching) the marker's data.
	runSSHEventually(t, h, sshKey, probe.Network.GuestIP, nil, "test", "-f", markerPath)
	restoreMinIO := stopWorkspaceBackend(t, minioService, h.Config.Workspace.Root)

	readCtx, cancelRead := context.WithTimeout(context.Background(), 5*time.Second)
	got, err := runSSH(readCtx, sshKey, probe.Network.GuestIP, h.GuestTCPPort, nil, "cat", markerPath)
	cancelRead()
	if err == nil {
		t.Fatalf("uncached read unexpectedly succeeded during MinIO outage: %q", got)
	}

	if err := restoreMinIO(); err != nil {
		t.Fatalf("restore %s: %v", minioService, err)
	}
	got = runSSHEventually(t, h, sshKey, probe.Network.GuestIP, nil, "cat", markerPath)
	if !bytes.Equal(got, marker) {
		t.Fatalf("post-MinIO-recovery read = %q, want %q", got, marker)
	}
	runSSHEventually(t, h, sshKey, probe.Network.GuestIP, nil, "rm", "-f", "--", markerPath)
	terminateWorkspaceVM(t, h, lifecycle, probe.ID)
}

func evictJuiceFSCache(path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output, err := exec.CommandContext(
		ctx,
		juicefsBinary,
		"warmup",
		"--evict",
		path,
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("evict JuiceFS cache for %s: %w: %s", path, err, output)
	}
	return nil
}

func requireStorageOutageQualification(t *testing.T) {
	t.Helper()
	switch enabled := os.Getenv(integrationStorageOutagesEnv); enabled {
	case "", "0":
		t.Skipf(
			"disruptive storage-outage qualification is disabled; set %s=1 on a dedicated host",
			integrationStorageOutagesEnv,
		)
	case "1":
	default:
		t.Fatalf("%s must be 0 or 1, got %q", integrationStorageOutagesEnv, enabled)
	}
}

func newWorkspaceFailureLifecycle(
	t *testing.T,
	h integrationEnvironment,
) (*microvm.Service, *supervisor.Supervisor, microvm.Store) {
	t.Helper()
	if h.Config.Workspace == nil {
		t.Fatal("storage-outage qualification requires workspace host configuration")
	}
	if err := preflight.Check(h.Config); err != nil {
		t.Fatalf("integration host prerequisites failed: %v", err)
	}
	if h.Config.Limits.MaxTotalMemoryMiB < h.MemoryMiB {
		t.Fatalf(
			"storage-outage qualification requires limits.max_total_memory_mib >= %d",
			h.MemoryMiB,
		)
	}

	store, err := microvm.OpenSQLiteStore(h.Config.StateDB)
	if err != nil {
		t.Fatalf("open isolated workspace-failure store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close isolated workspace-failure store: %v", err)
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
		t.Fatalf("construct workspace-failure lifecycle service: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), workspaceOperationTimeout(h))
		defer cancel()
		if err := lifecycle.TerminateAll(ctx); err != nil {
			t.Errorf("emergency workspace-failure cleanup: %v", err)
		}
	})
	return lifecycle, runtimeSupervisor, store
}

func stopWorkspaceBackend(
	t *testing.T,
	backendService string,
	workspaceRoot string,
) func() error {
	t.Helper()
	if err := systemctl(backendService, "is-active", "--quiet"); err != nil {
		t.Fatalf("storage service must be active before failure injection: %v", err)
	}
	if err := systemctl(backendService, "stop"); err != nil {
		t.Fatalf("stop storage service for failure injection: %v", err)
	}

	stopped := true
	restore := func() error {
		if !stopped {
			return nil
		}
		if err := systemctl(backendService, "start"); err != nil {
			return err
		}
		if err := systemctl(backendService, "is-active", "--quiet"); err != nil {
			return err
		}
		if err := systemctl(juicefsGatewayService, "is-active", "--quiet"); err != nil {
			return err
		}
		if err := waitForMountpoint(workspaceRoot); err != nil {
			return err
		}
		if err := systemctl(nfsGatewayService, "is-active", "--quiet"); err != nil {
			return err
		}
		stopped = false
		return nil
	}
	t.Cleanup(func() {
		if err := restore(); err != nil {
			t.Errorf(
				"emergency restore of %s and workspace gateway: %v",
				backendService,
				err,
			)
		}
	})
	return restore
}

func waitForMountpoint(path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := exec.CommandContext(
			ctx,
			"/usr/bin/mountpoint",
			"--quiet",
			path,
		).Run(); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for workspace mount %s: %w", path, ctx.Err())
		case <-ticker.C:
		}
	}
}

func systemctl(unit string, arguments ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	args := append(arguments, unit)
	output, err := exec.CommandContext(ctx, "/usr/bin/systemctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %v: %w: %s", args, err, output)
	}
	return nil
}
