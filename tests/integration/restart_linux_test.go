package integration

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/config"
	"github.com/stepandel/cantelop/fire-fuse/host/preflight"
	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
	"github.com/stepandel/cantelop/fire-fuse/runtime/recovery"
	"github.com/stepandel/cantelop/fire-fuse/runtime/supervisor"
)

const (
	crashHelperEnv     = "FIRE_FUSE_CRASH_HELPER"
	crashRuntimeEnv    = "FIRE_FUSE_CRASH_RUNTIME_DIR"
	crashStateEnv      = "FIRE_FUSE_CRASH_STATE_DB"
	crashResultEnv     = "FIRE_FUSE_CRASH_RESULT"
	crashParentPIDEnv  = "FIRE_FUSE_CRASH_PARENT_PID"
	crashHelperEnabled = "1"
)

func TestFirecrackerRestartRecovery(t *testing.T) {
	h := newIntegrationEnvironment(t)
	if err := preflight.Check(h.Config); err != nil {
		t.Fatalf("integration host prerequisites failed: %v", err)
	}
	if h.Config.Limits.MaxTotalMemoryMiB < h.MemoryMiB {
		t.Fatalf(
			"integration restart test requires limits.max_total_memory_mib >= %d",
			h.MemoryMiB,
		)
	}

	initialSupervisor, err := supervisor.New(h.Config, io.Discard)
	if err != nil {
		t.Fatalf("construct initial orphan checker: %v", err)
	}
	if err := initialSupervisor.CheckForOrphans(nil); err != nil {
		t.Fatalf("integration host must start without fire-fuse orphans: %v", err)
	}
	t.Cleanup(func() {
		if err := recoverAfterCrash(h); err != nil {
			t.Errorf("emergency restart-test recovery: %v", err)
		}
	})

	resultPath := filepath.Join(h.Config.RuntimeDir, "crash-result")
	logPath := filepath.Join(h.Config.RuntimeDir, "crash-helper.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatalf("create crash-helper log: %v", err)
	}
	t.Cleanup(func() {
		if err := logFile.Close(); err != nil {
			t.Errorf("close crash-helper log: %v", err)
		}
	})

	helperEnvironment := []string{
		crashHelperEnv + "=" + crashHelperEnabled,
		crashRuntimeEnv + "=" + h.Config.RuntimeDir,
		crashStateEnv + "=" + h.Config.StateDB,
		crashResultEnv + "=" + resultPath,
		crashParentPIDEnv + "=" + strconv.Itoa(os.Getpid()),
	}
	command := crashHelperCommand(helperEnvironment)
	command.Stdout = logFile
	command.Stderr = logFile
	if err := command.Run(); err != nil {
		t.Fatalf("crash helper failed: %v\n%s", err, readCrashLog(logPath))
	}

	vmIDBytes, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("read crash-helper result: %v\n%s", err, readCrashLog(logPath))
	}
	vmID := strings.TrimSpace(string(vmIDBytes))
	if vmID == "" {
		t.Fatalf("crash helper returned an empty VM ID\n%s", readCrashLog(logPath))
	}

	store, err := microvm.OpenSQLiteStore(h.Config.StateDB)
	if err != nil {
		t.Fatalf("reopen lifecycle store after crash: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close recovered lifecycle store: %v", err)
		}
	})
	recoverySupervisor, err := supervisor.New(h.Config, os.Stderr)
	if err != nil {
		t.Fatalf("construct restart recovery supervisor: %v", err)
	}

	running, err := store.Get(vmID)
	if err != nil {
		t.Fatalf("read lifecycle record left by crashed helper: %v", err)
	}
	if running.State != microvm.StateRunning {
		t.Fatalf("crash-surviving lifecycle state = %q, want %q", running.State, microvm.StateRunning)
	}
	if err := running.Runtime.Validate(); err != nil {
		t.Fatalf("validate crash-surviving runtime ownership: %v", err)
	}
	if running.Runtime.Process == (microvm.ProcessIdentity{}) {
		t.Fatal("crash-surviving record has no PID identity")
	}
	if err := recovery.CheckForOrphans(store, recoverySupervisor); err != nil {
		t.Fatalf("crash-surviving resources did not match durable ownership: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), restartRecoveryTimeout(h.Config))
	err = recovery.Reconcile(ctx, store, recoverySupervisor, time.Now)
	cancel()
	if err != nil {
		t.Fatalf("reconcile lifecycle after orchestrator crash: %v", err)
	}
	assertTerminatedCleanup(t, h, recoverySupervisor, store, running)
}

// TestFirecrackerCrashHelper runs only as a child of TestFirecrackerRestartRecovery.
// os.Exit deliberately bypasses defers so SQLite and runtime resources are left
// exactly as they would be after an abrupt orchestrator process exit.
func TestFirecrackerCrashHelper(t *testing.T) {
	parentPID := os.Getenv(crashParentPIDEnv)
	directParent := parentPID == strconv.Itoa(os.Getppid())
	delegatedParent := os.Getenv(integrationSystemdRunEnv) != "" && parentPID != ""
	if os.Getenv(crashHelperEnv) != crashHelperEnabled || (!directParent && !delegatedParent) {
		t.Skip("crash helper runs only in its dedicated subprocess")
	}

	cfg := crashHelperConfig(t)
	store, err := microvm.OpenSQLiteStore(cfg.StateDB)
	if err != nil {
		t.Fatalf("open crash-helper lifecycle store: %v", err)
	}
	runtimeSupervisor, err := supervisor.New(cfg, os.Stderr)
	if err != nil {
		t.Fatalf("construct crash-helper supervisor: %v", err)
	}
	lifecycle, err := microvm.NewServiceWithSupervisorAndLimits(
		store,
		runtimeSupervisor,
		microvm.AdmissionLimits{
			MaxVMs:              cfg.Limits.MaxVMs,
			MaxTotalMemoryMiB:   cfg.Limits.MaxTotalMemoryMiB,
			MaxConcurrentStarts: cfg.Limits.MaxConcurrentStarts,
		},
	)
	if err != nil {
		t.Fatalf("construct crash-helper lifecycle service: %v", err)
	}
	imageID := requiredEnvironment(t, integrationBuildIDEnv)
	if _, err := cfg.GetImage(imageID); err != nil {
		t.Fatalf("select crash-helper image %q: %v", imageID, err)
	}
	workspaceID := ensureIntegrationApp(t, store, "restart-recovery", "restart-recovery", imageID)
	machine, err := lifecycle.CreateAndStart(context.Background(), "sandbox-restart-recovery", microvm.CreateSpec{
		AppID: "restart-recovery", WorkspaceID: workspaceID,
	})
	if err != nil {
		t.Fatalf("start crash-helper Firecracker microVM: %v", err)
	}
	if err := writeCrashResult(requiredEnvironment(t, crashResultEnv), machine.ID); err != nil {
		t.Fatalf("persist crash-helper result: %v", err)
	}

	os.Exit(0)
}

func crashHelperCommand(helperEnvironment []string) *exec.Cmd {
	systemdRun := os.Getenv(integrationSystemdRunEnv)
	if systemdRun == "" {
		command := exec.Command(os.Args[0], "-test.run=^TestFirecrackerCrashHelper$", "-test.v")
		command.Env = append(os.Environ(), helperEnvironment...)
		return command
	}

	environment := append([]string{}, helperEnvironment...)
	for _, name := range []string{
		integrationConfigEnv,
		integrationBuildIDEnv,
		integrationGuestTCPPortEnv,
		integrationSystemdRunEnv,
	} {
		environment = append(environment, name+"="+os.Getenv(name))
	}
	arguments := []string{
		"--quiet", "--wait", "--pipe", "--collect", "--service-type=exec",
		"--property=Delegate=yes",
		"--unit=fire-fuse-crash-helper-" + strconv.Itoa(os.Getpid()),
	}
	for _, value := range environment {
		arguments = append(arguments, "--setenv="+value)
	}
	arguments = append(arguments, os.Args[0], "-test.run=^TestFirecrackerCrashHelper$", "-test.v")
	return exec.Command(systemdRun, arguments...)
}

func crashHelperConfig(t *testing.T) config.Config {
	t.Helper()
	cfg, err := config.LoadTrusted(requiredEnvironment(t, integrationConfigEnv))
	if err != nil {
		t.Fatalf("load crash-helper configuration: %v", err)
	}
	cfg.RuntimeDir = requiredEnvironment(t, crashRuntimeEnv)
	cfg.StateDB = requiredEnvironment(t, crashStateEnv)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate crash-helper configuration: %v", err)
	}
	return cfg
}

func writeCrashResult(path, vmID string) (err error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, file.Close())
	}()
	if _, err := fmt.Fprintln(file, vmID); err != nil {
		return err
	}
	return file.Sync()
}

func recoverAfterCrash(h integrationEnvironment) (err error) {
	store, err := microvm.OpenSQLiteStore(h.Config.StateDB)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, store.Close())
	}()
	runtimeSupervisor, err := supervisor.New(h.Config, io.Discard)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), restartRecoveryTimeout(h.Config))
	defer cancel()
	if err := recovery.Reconcile(ctx, store, runtimeSupervisor, time.Now); err != nil {
		return err
	}
	return recovery.CheckForOrphans(store, runtimeSupervisor)
}

func restartRecoveryTimeout(cfg config.Config) time.Duration {
	return time.Duration(cfg.Timeouts.Cleanup) + 15*time.Second
}

func readCrashLog(path string) string {
	contents, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("read helper log: %v", err)
	}
	return string(contents)
}
