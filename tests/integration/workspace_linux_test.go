package integration

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/host/preflight"
	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
	"github.com/stepandel/cantelop/fire-fuse/runtime/supervisor"
)

const integrationSSHKeyEnv = "FIRE_FUSE_INTEGRATION_SSH_KEY"

func TestWorkspaceIsolationAndPersistenceAcrossGuestLifecycles(t *testing.T) {
	h := newIntegrationEnvironment(t)
	if h.Config.Workspace == nil {
		t.Skip("workspace qualification requires workspace host configuration")
	}
	if h.GuestTCPPort == 0 {
		t.Fatalf("workspace qualification requires non-empty %s", integrationGuestTCPPortEnv)
	}
	if err := preflight.Check(h.Config); err != nil {
		t.Fatalf("integration host prerequisites failed: %v", err)
	}
	if h.Config.Limits.MaxVMs < 2 ||
		h.Config.Limits.MaxTotalMemoryMiB < 2*h.MemoryMiB {
		t.Fatalf(
			"workspace test requires capacity for two %d MiB, %d-vCPU VMs",
			h.MemoryMiB,
			h.VCPUs,
		)
	}

	sshKey := requiredEnvironment(t, integrationSSHKeyEnv)
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Fatalf("workspace integration requires ssh: %v", err)
	}
	keyInfo, err := os.Stat(sshKey)
	if err != nil {
		t.Fatalf("inspect integration SSH key: %v", err)
	}
	if !keyInfo.Mode().IsRegular() {
		t.Fatalf("integration SSH key is not a regular file: %s", sshKey)
	}
	sshKey, err = filepath.Abs(sshKey)
	if err != nil {
		t.Fatalf("resolve integration SSH key: %v", err)
	}

	store, err := microvm.OpenSQLiteStore(h.Config.StateDB)
	if err != nil {
		t.Fatalf("open isolated workspace store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close isolated workspace store: %v", err)
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
			t.Errorf("emergency workspace integration cleanup: %v", err)
		}
	})

	const workspaceID = "staging-shared"
	writer := startWorkspaceVM(t, h, lifecycle, store, workspaceID)
	isolated := startWorkspaceVM(t, h, lifecycle, store, "staging-isolated")
	markerPath := "/workspace/.fire-fuse-integration-" + writer.ID
	marker := []byte("fire-fuse shared workspace\n")
	isolatedMarkerPath := "/workspace/.fire-fuse-isolated-" + isolated.ID
	isolatedMarker := []byte("fire-fuse isolated workspace\n")

	runSSHEventually(
		t, h, sshKey, writer.Network.GuestIP, marker,
		"dd", "of="+markerPath, "status=none", "conv=fsync",
	)
	runSSHEventually(
		t, h, sshKey, isolated.Network.GuestIP, nil,
		"test", "!", "-e", markerPath,
	)
	runSSHEventually(
		t, h, sshKey, isolated.Network.GuestIP, isolatedMarker,
		"dd", "of="+isolatedMarkerPath, "status=none", "conv=fsync",
	)
	runSSHEventually(
		t, h, sshKey, writer.Network.GuestIP, nil,
		"test", "!", "-e", isolatedMarkerPath,
	)

	// These checks run only on an explicitly enabled qualification host.
	// Internal command aliases must fail even below the exported directory.
	for _, name := range []string{".control", ".jfs.control"} {
		runSSHEventually(t, h, sshKey, writer.Network.GuestIP, nil,
			"test", "!", "-e", "/workspace/"+name)
	}
	// Squashing must preserve the file owner's normal mode and rename operations.
	runSSHEventually(t, h, sshKey, writer.Network.GuestIP, nil, "chmod", "0600", markerPath)
	runSSHEventually(t, h, sshKey, writer.Network.GuestIP, nil, "mv", markerPath, markerPath+".renamed")
	runSSHEventually(t, h, sshKey, writer.Network.GuestIP, nil, "mv", markerPath+".renamed", markerPath)
	if got := runSSHEventually(t, h, sshKey, writer.Network.GuestIP, nil, "cat", markerPath); !bytes.Equal(got, marker) {
		t.Fatalf("owner-only file after rename = %q, want %q", got, marker)
	}

	writerExport := filepath.Join(h.Config.Workspace.Root, "wsp_"+workspaceID)
	isolatedExport := filepath.Join(h.Config.Workspace.Root, "wsp_staging-isolated")
	assertNFSAccessDenied(
		t,
		h,
		sshKey,
		isolated.Network.GuestIP,
		isolated.Network.GatewayIP,
		writerExport,
		"",
		"",
	)
	runSSHEventually(
		t, h, sshKey, writer.Network.GuestIP, nil,
		"chmod", "0777", "/workspace",
	)
	writerModeRestored := false
	defer func() {
		if !writerModeRestored {
			runSSHEventually(
				t, h, sshKey, writer.Network.GuestIP, nil,
				"chmod", "0700", "/workspace",
			)
		}
	}()
	assertNFSAccessDenied(
		t,
		h,
		sshKey,
		isolated.Network.GuestIP,
		isolated.Network.GatewayIP,
		h.Config.Workspace.Root,
		"",
		filepath.Join("wsp_"+workspaceID, filepath.Base(markerPath)),
	)
	runSSHEventually(
		t, h, sshKey, writer.Network.GuestIP, nil,
		"chmod", "0700", "/workspace",
	)
	writerModeRestored = true
	assertNFSAccessDenied(
		t,
		h,
		sshKey,
		writer.Network.GuestIP,
		writer.Network.GatewayIP,
		isolatedExport,
		isolated.Network.GuestIP,
		"",
	)

	runSSHEventually(
		t, h, sshKey, isolated.Network.GuestIP, nil,
		"rm", "-f", "--", isolatedMarkerPath,
	)
	terminateWorkspaceVM(t, h, lifecycle, isolated.ID)

	reader := startWorkspaceVM(t, h, lifecycle, store, workspaceID)
	got := runSSHEventually(t, h, sshKey, reader.Network.GuestIP, nil, "cat", markerPath)
	if !bytes.Equal(got, marker) {
		t.Fatalf("concurrent guest read = %q, want %q", got, marker)
	}

	terminateWorkspaceVM(t, h, lifecycle, writer.ID)
	terminateWorkspaceVM(t, h, lifecycle, reader.ID)

	restarted := startWorkspaceVM(t, h, lifecycle, store, workspaceID)
	got = runSSHEventually(t, h, sshKey, restarted.Network.GuestIP, nil, "cat", markerPath)
	if !bytes.Equal(got, marker) {
		t.Fatalf("post-lifecycle guest read = %q, want %q", got, marker)
	}
	runSSHEventually(t, h, sshKey, restarted.Network.GuestIP, nil, "rm", "-f", "--", markerPath)
	terminateWorkspaceVM(t, h, lifecycle, restarted.ID)
}

func startWorkspaceVM(
	t *testing.T,
	h integrationEnvironment,
	lifecycle *microvm.Service,
	store microvm.Store,
	workspaceID string,
) microvm.Sandbox {
	t.Helper()
	registeredWorkspaceID := ensureIntegrationApp(t, store, workspaceID, workspaceID, h.BuildID)
	ctx, cancel := context.WithTimeout(
		context.Background(),
		time.Duration(h.Config.Timeouts.Startup)+time.Second,
	)
	defer cancel()
	machine, err := lifecycle.CreateAndStart(ctx, nextIntegrationSandboxID(), microvm.CreateSpec{
		AppID: workspaceID, WorkspaceID: registeredWorkspaceID,
	})
	if err != nil {
		t.Fatalf("start workspace-enabled Firecracker microVM: %v", err)
	}
	return machine
}

func terminateWorkspaceVM(
	t *testing.T,
	h integrationEnvironment,
	lifecycle *microvm.Service,
	vmID string,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), workspaceOperationTimeout(h))
	defer cancel()
	machine, err := lifecycle.Terminate(ctx, vmID)
	if err != nil {
		t.Fatalf("terminate workspace-enabled Firecracker microVM: %v", err)
	}
	if machine.State != microvm.StateTerminated {
		t.Fatalf("terminated lifecycle state = %q, want %q", machine.State, microvm.StateTerminated)
	}
}

func runSSHEventually(
	t *testing.T,
	h integrationEnvironment,
	keyPath string,
	guestIP string,
	input []byte,
	remoteCommand ...string,
) []byte {
	t.Helper()
	deadline := time.Now().Add(time.Duration(h.Config.Timeouts.Startup))
	var lastErr error
	for time.Now().Before(deadline) {
		attemptCtx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		output, err := runSSH(attemptCtx, keyPath, guestIP, h.GuestTCPPort, input, remoteCommand...)
		cancel()
		if err == nil {
			return output
		}
		lastErr = err
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("SSH command %q did not succeed for guest %s: %v", remoteCommand[0], guestIP, lastErr)
	return nil
}

func runSSH(
	ctx context.Context,
	keyPath string,
	guestIP string,
	guestTCPPort int,
	input []byte,
	remoteCommand ...string,
) ([]byte, error) {
	args := []string{
		"-i", keyPath,
		"-p", strconv.Itoa(guestTCPPort),
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=2",
		"-o", "GlobalKnownHostsFile=/dev/null",
		"-o", "IdentitiesOnly=yes",
		"-o", "LogLevel=ERROR",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"root@" + guestIP,
	}
	args = append(args, remoteCommand...)
	command := exec.CommandContext(ctx, "ssh", args...)
	command.Stdin = bytes.NewReader(input)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, stderr.String())
	}
	return output, nil
}

func assertNFSAccessDenied(
	t *testing.T,
	h integrationEnvironment,
	keyPath string,
	guestIP string,
	gatewayIP string,
	exportPath string,
	spoofIP string,
	forbiddenRelativePath string,
) {
	t.Helper()
	spoofArgument := spoofIP
	if spoofArgument == "" {
		spoofArgument = "-"
	}
	forbiddenArgument := forbiddenRelativePath
	if forbiddenArgument == "" {
		forbiddenArgument = "-"
	}
	const denialProbe = `set -eu
gateway_ip=$1
export_path=$2
spoof_ip=$3
forbidden_path=$4
mount_dir=/run/fire-fuse-denied-nfs
test -x /usr/sbin/mount.nfs4
test -x /usr/bin/timeout
test -x /usr/sbin/ip
mkdir -p "$mount_dir"
spoof_added=0
cleanup() {
  /usr/bin/umount "$mount_dir" >/dev/null 2>&1 || true
  if [ "$spoof_added" = "1" ]; then
    /usr/sbin/ip address delete "$spoof_ip/32" dev eth0 >/dev/null 2>&1 || true
  fi
  rmdir "$mount_dir" >/dev/null 2>&1 || true
}
trap cleanup EXIT
if [ "$spoof_ip" != "-" ]; then
  if ! /usr/sbin/ip address add "$spoof_ip/32" dev eth0; then
    echo "guest capability boundary rejected source spoofing" >&2
    exit 0
  fi
  spoof_added=1
fi
if /usr/bin/timeout 8 /usr/sbin/mount.nfs4 \
  -o vers=4.2,proto=tcp \
  "$gateway_ip:$export_path" \
  "$mount_dir"
then
  if [ "$forbidden_path" = "-" ]; then
    echo "unauthorized NFS mount succeeded" >&2
    exit 1
  fi
  if /usr/bin/cat "$mount_dir/$forbidden_path" >/dev/null 2>&1; then
    echo "unauthorized NFS file read succeeded" >&2
    exit 1
  fi
fi
`
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	if _, err := runSSH(
		ctx,
		keyPath,
		guestIP,
		h.GuestTCPPort,
		[]byte(denialProbe),
		"sh",
		"-s",
		"--",
		gatewayIP,
		exportPath,
		spoofArgument,
		forbiddenArgument,
	); err != nil {
		t.Fatalf(
			"unauthorized NFS access probe from %s to %s:%s with spoofed source %q and forbidden path %q: %v",
			guestIP,
			gatewayIP,
			exportPath,
			spoofIP,
			forbiddenRelativePath,
			err,
		)
	}
}

func workspaceOperationTimeout(h integrationEnvironment) time.Duration {
	return time.Duration(
		h.Config.Timeouts.GuestShutdown+
			h.Config.Timeouts.ShutdownGrace+
			h.Config.Timeouts.ShutdownForce+
			h.Config.Timeouts.Cleanup,
	) + time.Second
}
