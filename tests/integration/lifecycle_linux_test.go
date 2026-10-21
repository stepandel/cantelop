package integration

import (
	"context"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/host/firecracker"
	"github.com/stepandel/cantelop/fire-fuse/host/network"
	"github.com/stepandel/cantelop/fire-fuse/host/preflight"
	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
	"github.com/stepandel/cantelop/fire-fuse/runtime/supervisor"
)

func TestFirecrackerLifecycle(t *testing.T) {
	h := newIntegrationEnvironment(t)
	if err := preflight.Check(h.Config); err != nil {
		t.Fatalf("integration host prerequisites failed: %v", err)
	}
	if h.Config.Limits.MaxTotalMemoryMiB < h.MemoryMiB {
		t.Fatalf(
			"integration lifecycle test requires limits.max_total_memory_mib >= %d",
			h.MemoryMiB,
		)
	}

	store, err := microvm.OpenSQLiteStore(h.Config.StateDB)
	if err != nil {
		t.Fatalf("open isolated lifecycle store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close isolated lifecycle store: %v", err)
		}
	})
	workspaceID := ensureIntegrationApp(t, store, "lifecycle", "lifecycle", h.BuildID)

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
		ctx, cancel := context.WithTimeout(
			context.Background(),
			time.Duration(h.Config.Timeouts.Cleanup),
		)
		defer cancel()
		if err := lifecycle.TerminateAll(ctx); err != nil {
			t.Errorf("emergency integration cleanup: %v", err)
		}
	})

	startCtx, cancelStart := context.WithTimeout(
		context.Background(),
		time.Duration(h.Config.Timeouts.Startup)+time.Second,
	)
	running, err := lifecycle.CreateAndStart(startCtx, "sandbox-lifecycle", microvm.CreateSpec{
		AppID: "lifecycle", WorkspaceID: workspaceID,
	})
	cancelStart()
	if err != nil {
		t.Fatalf("start real Firecracker microVM: %v", err)
	}
	if running.State != microvm.StateRunning {
		t.Fatalf("started lifecycle state = %q, want %q", running.State, microvm.StateRunning)
	}

	persisted, err := store.Get(running.ID)
	if err != nil {
		t.Fatalf("read durable running record: %v", err)
	}
	assertRunningOwnership(t, h, runtimeSupervisor, persisted)
	assertGuestTCPReachable(t, h, persisted)
	if err := runtimeSupervisor.CheckForOrphans([]microvm.Sandbox{persisted}); err != nil {
		t.Fatalf("running resources did not match durable ownership: %v", err)
	}
	done, exists := runtimeSupervisor.RuntimeDone(running.ID)
	if !exists {
		t.Fatal("running Firecracker process has no completion signal")
	}

	stopCtx, cancelStop := context.WithTimeout(
		context.Background(),
		time.Duration(h.Config.Timeouts.Cleanup),
	)
	terminated, err := lifecycle.Terminate(stopCtx, running.ID)
	cancelStop()
	if err != nil {
		t.Fatalf("terminate real Firecracker microVM: %v", err)
	}
	if terminated.State != microvm.StateTerminated {
		t.Fatalf("terminated lifecycle state = %q, want %q", terminated.State, microvm.StateTerminated)
	}
	select {
	case <-done:
	default:
		t.Fatal("Firecracker process completion signal remained open after termination")
	}
	assertTerminatedCleanup(t, h, runtimeSupervisor, store, persisted)
}

func assertGuestTCPReachable(t *testing.T, h integrationEnvironment, machine microvm.Sandbox) {
	t.Helper()
	if h.GuestTCPPort == 0 {
		t.Logf("guest reachability probe disabled; set %s to require a guest TCP service", integrationGuestTCPPortEnv)
		return
	}
	if machine.Network.GuestIP == "" {
		t.Fatal("running machine has no guest IP for reachability probe")
	}

	address := net.JoinHostPort(machine.Network.GuestIP, strconv.Itoa(h.GuestTCPPort))
	deadline := time.Now().Add(time.Duration(h.Config.Timeouts.Startup))
	var lastErr error
	for time.Now().Before(deadline) {
		connection, dialErr := net.DialTimeout("tcp", address, 500*time.Millisecond)
		if dialErr == nil {
			if closeErr := connection.Close(); closeErr != nil {
				t.Fatalf("close guest reachability connection to %s: %v", address, closeErr)
			}
			t.Logf("guest TCP service reachable at %s", address)
			return
		}
		lastErr = dialErr
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("guest TCP service %s did not become reachable: %v", address, lastErr)
}

func assertRunningOwnership(
	t *testing.T,
	h integrationEnvironment,
	runtimeSupervisor *supervisor.Supervisor,
	machine microvm.Sandbox,
) {
	t.Helper()
	if machine.State != microvm.StateRunning {
		t.Fatalf("durable lifecycle state = %q, want %q", machine.State, microvm.StateRunning)
	}
	if err := machine.Runtime.Validate(); err != nil {
		t.Fatalf("validate durable runtime ownership: %v", err)
	}
	if !machine.Runtime.JailPrepared || !machine.Runtime.AddressAllocated ||
		!machine.Runtime.TAPCreated || !machine.Runtime.ForwardingApplied ||
		machine.Runtime.Process == (microvm.ProcessIdentity{}) {
		t.Fatalf("durable running ownership is incomplete: %#v", machine.Runtime)
	}
	if machine.Network == (microvm.NetworkAssignment{}) || machine.Network != machine.Runtime.Network {
		t.Fatalf("guest network does not match durable ownership: %#v", machine)
	}
	if !runtimeSupervisor.IsRunning(machine.ID) {
		t.Fatal("production supervisor does not report the Firecracker process running")
	}

	layout, err := firecracker.NewJailLayout(h.Config.RuntimeDir, h.Config.FirecrackerBin, machine.ID)
	if err != nil {
		t.Fatalf("derive running jail layout: %v", err)
	}
	if err := layout.ValidateRuntimeManifest(); err != nil {
		t.Fatalf("validate running jail ownership manifest: %v", err)
	}
	socket, err := os.Stat(layout.HostAPISocket)
	if err != nil {
		t.Fatalf("inspect running Firecracker API socket: %v", err)
	}
	if socket.Mode()&os.ModeSocket == 0 {
		t.Fatalf("Firecracker API path mode = %s, want Unix socket", socket.Mode())
	}

	expectedTAP, err := network.TAPName(machine.ID)
	if err != nil {
		t.Fatalf("derive expected TAP name: %v", err)
	}
	if machine.Runtime.TAPName != expectedTAP {
		t.Fatalf("durable TAP name = %q, want %q", machine.Runtime.TAPName, expectedTAP)
	}
	expectedAlias, err := network.TAPAlias(machine.ID)
	if err != nil {
		t.Fatalf("derive expected TAP alias: %v", err)
	}
	alias, err := network.ReadTAPAlias(expectedTAP)
	if err != nil {
		t.Fatalf("read running TAP ownership alias: %v", err)
	}
	if alias != expectedAlias {
		t.Fatalf("running TAP alias = %q, want %q", alias, expectedAlias)
	}
	// ListOwnedRules returns unique ownership records rather than every raw
	// iptables rule. A live guest owns one egress-chain record plus the IPv4
	// INPUT, IPv4 FORWARD, and IPv4 NAT surfaces.
	assertFirewallRuleCount(t, h, expectedTAP, 4)
}

func assertTerminatedCleanup(
	t *testing.T,
	h integrationEnvironment,
	runtimeSupervisor *supervisor.Supervisor,
	store microvm.Store,
	running microvm.Sandbox,
) {
	t.Helper()
	persisted, err := store.Get(running.ID)
	if err != nil {
		t.Fatalf("read durable terminated record: %v", err)
	}
	if persisted.State != microvm.StateTerminated {
		t.Fatalf("durable lifecycle state = %q, want %q", persisted.State, microvm.StateTerminated)
	}
	if persisted.Runtime != (microvm.RuntimeOwnership{}) {
		t.Fatalf("terminated runtime ownership = %#v, want empty", persisted.Runtime)
	}
	if runtimeSupervisor.IsRunning(running.ID) {
		t.Fatal("production supervisor still reports Firecracker running after termination")
	}

	layout, err := firecracker.NewJailLayout(h.Config.RuntimeDir, h.Config.FirecrackerBin, running.ID)
	if err != nil {
		t.Fatalf("derive terminated jail layout: %v", err)
	}
	if _, err := os.Lstat(layout.VMDir); !os.IsNotExist(err) {
		t.Fatalf("terminated jail path still exists or cannot be inspected: %v", err)
	}
	if _, err := net.InterfaceByName(running.Runtime.TAPName); err == nil {
		t.Fatalf("terminated TAP %q still exists", running.Runtime.TAPName)
	}
	assertFirewallRuleCount(t, h, running.Runtime.TAPName, 0)
	if err := runtimeSupervisor.CheckForOrphans([]microvm.Sandbox{persisted}); err != nil {
		t.Fatalf("host resources remained after lifecycle termination: %v", err)
	}
}

func assertFirewallRuleCount(t *testing.T, h integrationEnvironment, tapName string, want int) {
	t.Helper()
	policy, err := network.NewEgressPolicy(
		h.Config.Network.Egress.DNSResolvers,
		h.Config.Network.Egress.AllowedTCPPorts,
		h.Config.Network.Egress.AllowedUDPPorts,
	)
	if err != nil {
		t.Fatalf("construct egress policy: %v", err)
	}
	manager, err := network.NewForwardingManager(
		h.Config.Network.IPTablesBin,
		h.Config.Network.IP6TablesBin,
		policy,
	)
	if err != nil {
		t.Fatalf("construct forwarding inventory: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(h.Config.Timeouts.Cleanup))
	defer cancel()
	rules, err := manager.ListOwnedRules(ctx)
	if err != nil {
		t.Fatalf("inventory owned firewall rules: %v", err)
	}
	comment := network.FirewallOwnershipComment(tapName)
	got := 0
	for _, rule := range rules {
		if rule.Comment == comment {
			got++
		}
	}
	if got != want {
		t.Fatalf("owned firewall rule count for %q = %d, want %d", tapName, got, want)
	}
}
