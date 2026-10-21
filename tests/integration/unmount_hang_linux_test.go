package integration

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
)

const integrationUnmountHangEnv = "FIRE_FUSE_INTEGRATION_UNMOUNT_HANG"

func TestWorkspaceUnmountHangFallsBackToForcedTermination(t *testing.T) {
	requireUnmountHangQualification(t)
	h := newIntegrationEnvironment(t)
	lifecycle, runtimeSupervisor, store := newWorkspaceFailureLifecycle(t, h)
	sshKey := requiredEnvironment(t, integrationSSHKeyEnv)

	running := startWorkspaceVM(t, h, lifecycle, store, "unmount-hang")
	installHangingWorkspaceStop(t, h, sshKey, running.Network.GuestIP)

	stopCtx, cancelStop := context.WithTimeout(
		context.Background(),
		workspaceOperationTimeout(h),
	)
	started := time.Now()
	terminated, err := lifecycle.Terminate(stopCtx, running.ID)
	elapsed := time.Since(started)
	cancelStop()
	if err != nil {
		t.Fatalf("terminate workspace with hanging unmount: %v", err)
	}
	if terminated.State != microvm.StateTerminated {
		t.Fatalf("terminated lifecycle state = %q, want %q", terminated.State, microvm.StateTerminated)
	}

	guestShutdownTimeout := time.Duration(h.Config.Timeouts.GuestShutdown)
	if elapsed < guestShutdownTimeout {
		t.Fatalf(
			"termination completed in %s before the %s guest-shutdown deadline; hanging ExecStop was not exercised",
			elapsed,
			guestShutdownTimeout,
		)
	}
	assertTerminatedCleanup(t, h, runtimeSupervisor, store, running)
}

func requireUnmountHangQualification(t *testing.T) {
	t.Helper()
	switch enabled := os.Getenv(integrationUnmountHangEnv); enabled {
	case "", "0":
		t.Skipf(
			"disruptive unmount-hang qualification is disabled; set %s=1 on a dedicated host",
			integrationUnmountHangEnv,
		)
	case "1":
	default:
		t.Fatalf("%s must be 0 or 1, got %q", integrationUnmountHangEnv, enabled)
	}
}

// installHangingWorkspaceStop blocks the guest's /workspace unmount during
// shutdown. The assignment agent mounts /workspace with mount.nfs4, so systemd
// tracks it as the runtime-discovered workspace.mount unit and unmounts it only
// after every unit that RequiresMountsFor=/workspace has stopped. A transient
// unit with that dependency and a hanging ExecStop therefore holds the NFS
// unmount open past the host's graceful guest-shutdown deadline. The unit lives
// only in the guest's /run, so the injection disappears with the microVM.
func installHangingWorkspaceStop(
	t *testing.T,
	h integrationEnvironment,
	sshKey string,
	guestIP string,
) {
	t.Helper()
	const unit = "fire-fuse-unmount-hang.service"
	runSSHEventually(t, h, sshKey, guestIP, nil, "mountpoint", "-q", "/workspace")
	runSSHEventually(
		t,
		h,
		sshKey,
		guestIP,
		nil,
		"systemd-run",
		"--unit="+unit,
		"--remain-after-exit",
		"--property=RequiresMountsFor=/workspace",
		"--property=ExecStop='/bin/sleep 300'",
		"--property=TimeoutStopSec=5min",
		"/bin/true",
	)
	runSSHEventually(t, h, sshKey, guestIP, nil, "systemctl", "is-active", "--quiet", unit)
	properties := string(runSSHEventually(
		t,
		h,
		sshKey,
		guestIP,
		nil,
		"systemctl",
		"show",
		unit,
		"--property=ActiveState",
		"--property=After",
		"--property=ExecStop",
	))
	for _, want := range []string{"ActiveState=active", "workspace.mount", "/bin/sleep 300"} {
		if !strings.Contains(properties, want) {
			t.Fatalf("injected unmount hang unit is missing %q: %q", want, properties)
		}
	}
}
