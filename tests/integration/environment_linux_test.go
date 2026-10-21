package integration

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/config"
	"github.com/stepandel/cantelop/fire-fuse/host/firecracker"
	"github.com/stepandel/cantelop/fire-fuse/host/preflight"
	"github.com/stepandel/cantelop/fire-fuse/host/snapshotbundle"
	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
)

const (
	integrationEnabledEnv      = "FIRE_FUSE_INTEGRATION"
	integrationConfigEnv       = "FIRE_FUSE_INTEGRATION_CONFIG"
	integrationBuildIDEnv      = "FIRE_FUSE_INTEGRATION_BUILD_ID"
	integrationGuestTCPPortEnv = "FIRE_FUSE_INTEGRATION_GUEST_TCP_PORT"
	integrationSystemdRunEnv   = "FIRE_FUSE_INTEGRATION_SYSTEMD_RUN"
)

type integrationEnvironment struct {
	Config       config.Config
	Profile      string
	BuildID      string
	VCPUs        int
	MemoryMiB    int
	GuestTCPPort int
}

var integrationSandboxSequence atomic.Uint64

func ensureIntegrationApp(t *testing.T, store microvm.Store, appID, workspaceID, buildID string) string {
	t.Helper()
	now := time.Now().UTC()
	_, err := store.EnsureComputeTenant(microvm.ComputeTenant{ID: "usr_integration", CreatedAt: now})
	if err != nil {
		t.Fatalf("create integration compute tenant: %v", err)
	}
	app := microvm.App{ID: appID, Slug: appID, CreatedAt: now, UpdatedAt: now}
	workspace := microvm.Workspace{ID: "wsp_" + workspaceID, AppID: appID, Slug: "test", CreatedAt: now, UpdatedAt: now}
	sessionRuntime := microvm.AppSessionRuntime{ActiveBuildID: buildID, Runtime: microvm.AppRuntimeSpec{Profile: microvm.DefaultSandboxProfileName, InternalPort: 3000}}
	build := microvm.AppBuild{AppID: appID, BuildID: buildID, CreatedAt: now}
	err = store.CreateAppForComputeTenant("usr_integration", app, sessionRuntime, build)
	if err != nil && !errors.Is(err, microvm.ErrAlreadyExists) {
		t.Fatalf("create integration app: %v", err)
	}
	err = store.CreateWorkspace(workspace)
	if err != nil && !errors.Is(err, microvm.ErrAlreadyExists) {
		t.Fatalf("create integration workspace: %v", err)
	}
	return workspace.ID
}

func nextIntegrationSandboxID() string {
	return fmt.Sprintf("sandbox-%d", integrationSandboxSequence.Add(1))
}

func TestFirecrackerIntegrationPrerequisites(t *testing.T) {
	h := newIntegrationEnvironment(t)
	if err := preflight.Check(h.Config); err != nil {
		t.Fatalf("integration host prerequisites failed: %v", err)
	}
	t.Logf(
		"Firecracker integration prerequisites ready: profile=%s runtime=%s",
		h.Profile,
		h.Config.RuntimeDir,
	)
}

func newIntegrationEnvironment(t *testing.T) integrationEnvironment {
	t.Helper()
	switch enabled := os.Getenv(integrationEnabledEnv); enabled {
	case "", "0":
		t.Skipf("real Firecracker integration is disabled; set %s=1 on a dedicated Linux KVM host", integrationEnabledEnv)
	case "1":
		// Continue. Once explicitly enabled, missing prerequisites are failures.
	default:
		t.Fatalf("%s must be 0 or 1, got %q", integrationEnabledEnv, enabled)
	}
	if os.Geteuid() != 0 {
		t.Fatalf("%s=1 currently requires root for TAP and iptables integration", integrationEnabledEnv)
	}

	configPath := requiredEnvironment(t, integrationConfigEnv)
	cfg, err := config.LoadTrusted(configPath)
	if err != nil {
		t.Fatalf("load integration configuration: %v", err)
	}
	profile, err := cfg.GetProfile(microvm.DefaultSandboxProfileName)
	if err != nil {
		t.Fatalf("select default integration profile: %v", err)
	}

	runtimeDir, err := os.MkdirTemp("/var", "ff-")
	if err != nil {
		t.Fatalf("create short integration runtime directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(runtimeDir); err != nil {
			t.Errorf("remove integration runtime directory: %v", err)
		}
	})
	cfg.RuntimeDir = runtimeDir
	cfg.StateDB = filepath.Join(runtimeDir, "state.db")
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate isolated integration configuration: %v", err)
	}
	if _, err := firecracker.NewJailLayout(
		cfg.RuntimeDir,
		cfg.FirecrackerBin,
		"00000000-0000-4000-8000-000000000000",
	); err != nil {
		t.Fatalf("isolated integration runtime cannot fit a Firecracker jail layout: %v", err)
	}

	buildID := requiredEnvironment(t, integrationBuildIDEnv)
	if _, err := cfg.GetImage(buildID); err != nil {
		t.Fatalf("select integration build %q: %v", buildID, err)
	}
	vcpus, memoryMiB := snapshotShape(t, profile)
	if vcpus != microvm.DefaultSandboxVCPUs || memoryMiB != microvm.DefaultSandboxMemoryMiB {
		t.Fatalf(
			"default integration profile snapshot is %d vCPU/%d MiB, want %d/%d",
			vcpus, memoryMiB, microvm.DefaultSandboxVCPUs, microvm.DefaultSandboxMemoryMiB,
		)
	}
	return integrationEnvironment{
		Config:       cfg,
		Profile:      microvm.DefaultSandboxProfileName,
		BuildID:      buildID,
		VCPUs:        vcpus,
		MemoryMiB:    memoryMiB,
		GuestTCPPort: optionalGuestTCPPort(t),
	}
}

func optionalGuestTCPPort(t *testing.T) int {
	t.Helper()
	value := os.Getenv(integrationGuestTCPPortEnv)
	if value == "" {
		return 0
	}
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		t.Fatalf("%s must be a TCP port from 1 through 65535, got %q", integrationGuestTCPPortEnv, value)
	}
	return port
}

func snapshotShape(t *testing.T, profile config.Profile) (int, int) {
	t.Helper()
	manifest, err := snapshotbundle.Load(filepath.Join(
		profile.SnapshotBundle,
		snapshotbundle.ManifestName,
	))
	if err != nil {
		t.Fatalf("load integration snapshot bundle: %v", err)
	}
	return manifest.Machine.VCPUCount, manifest.Machine.MemoryMiB
}

func requiredEnvironment(t *testing.T, name string) string {
	t.Helper()
	value, exists := os.LookupEnv(name)
	if !exists || value == "" {
		t.Fatalf("%s=1 requires non-empty %s", integrationEnabledEnv, name)
	}
	return value
}
