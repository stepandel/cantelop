package guestassignment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/stepandel/cantelop/fire-fuse/guest-runtime/guestconfig"
)

const (
	EventClockSynchronized = "clock_synchronized"
	EventNetworkConfigured = "network_configured"
	EventContainerMounted  = "container_mounted"
	EventWorkspaceMounted  = "workspace_mounted"
	EventWorkloadStarted   = "workload_started"
	EventSDKListenerReady  = "sdk_listener_ready"
)

// Milestone reports a completed assignment step.
type Milestone func(string) error

type commandRunner interface {
	Run(context.Context, string, ...string) error
}

type systemCommandRunner struct{}

func (systemCommandRunner) Run(ctx context.Context, name string, arguments ...string) error {
	command := exec.CommandContext(ctx, name, arguments...)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command.Run()
}

type runtimePaths struct {
	workloadConfig         string
	workspaceConfig        string
	ip                     string
	prepareContainerDevice string
	mountContainer         string
	unmountContainer       string
	waitWorkspaceNetwork   string
	mountWorkspace         string
	waitWorkspace          string
	umount                 string
	systemctl              string
	workloadUnit           string
}

var defaultRuntimePaths = runtimePaths{
	workloadConfig:         "/run/fire-fuse/workload.json",
	workspaceConfig:        "/run/fire-fuse/workspace",
	ip:                     "/usr/sbin/ip",
	prepareContainerDevice: "/usr/local/sbin/fire-fuse-prepare-container-device",
	mountContainer:         "/usr/local/sbin/fire-fuse-mount-container-rootfs",
	unmountContainer:       "/usr/local/sbin/fire-fuse-unmount-container-rootfs",
	waitWorkspaceNetwork:   "/usr/local/sbin/fire-fuse-wait-workspace-network",
	mountWorkspace:         "/usr/local/sbin/fire-fuse-mount-workspace",
	waitWorkspace:          "/usr/local/sbin/fire-fuse-wait-workspace",
	umount:                 "/usr/bin/umount",
	systemctl:              "/usr/bin/systemctl",
	workloadUnit:           "fire-fuse-assigned-workload.service",
}

// Apply installs one assignment and starts its workload.
func Apply(ctx context.Context, assignment Assignment, milestone Milestone) error {
	return apply(
		ctx,
		assignment,
		milestone,
		setRealtimeClock,
		systemCommandRunner{},
		defaultRuntimePaths,
		startSDKBridge,
	)
}

func apply(
	ctx context.Context,
	assignment Assignment,
	milestone Milestone,
	setClock func(int64) error,
	runner commandRunner,
	paths runtimePaths,
	startBridge sdkBridgeStarter,
) error {
	if err := assignment.Validate(); err != nil {
		return err
	}
	if milestone == nil {
		milestone = func(string) error { return nil }
	}
	if setClock == nil {
		return errors.New("clock setter is required")
	}
	if startBridge == nil {
		return errors.New("SDK bridge starter is required")
	}
	if err := setClock(assignment.ClockUnixNano); err != nil {
		return fmt.Errorf("synchronize realtime clock: %w", err)
	}
	if err := milestone(EventClockSynchronized); err != nil {
		return fmt.Errorf("report clock synchronization: %w", err)
	}
	if err := configureNetwork(ctx, assignment.Network, runner, paths.ip); err != nil {
		return err
	}
	if err := milestone(EventNetworkConfigured); err != nil {
		return fmt.Errorf("report network configuration: %w", err)
	}

	listenerPort, sdkWorkload, err := guestconfig.SDKListenerPort(assignment.Workload.Process.Env)
	if err != nil {
		return fmt.Errorf("resolve SDK listener readiness: %w", err)
	}
	var bridge sdkBridge
	if sdkWorkload {
		targetPort := sdkBridgeTargetPort
		if listenerPort == targetPort {
			targetPort = sdkBridgeFallbackTargetPort
		}
		bridge, err = startBridge(
			ctx,
			assignment.Network.Address,
			listenerPort,
			targetPort,
		)
		if err != nil {
			return fmt.Errorf("start SDK gateway bridge: %w", err)
		}
		defer bridge.Close()
		assignment.Workload.Process.Env = setEnvironment(assignment.Workload.Process.Env, "CANTELOP_SANDBOX_ID", assignment.AssignmentID)
		assignment.Workload.Process.Env = setEnvironment(
			assignment.Workload.Process.Env,
			guestconfig.InternalPortEnvironmentName,
			strconv.Itoa(targetPort),
		)
	}
	if err := installConfiguration(assignment, paths); err != nil {
		return err
	}

	containerMounted := false
	workspaceMounted := false
	workloadStarted := false
	defer func() {
		if workloadStarted {
			return
		}
		if workspaceMounted {
			_ = runner.Run(context.WithoutCancel(ctx), paths.umount, "/workspace")
		}
		if containerMounted {
			_ = runner.Run(context.WithoutCancel(ctx), paths.unmountContainer)
		}
	}()

	if err := runStep(ctx, runner, paths.prepareContainerDevice); err != nil {
		return fmt.Errorf("prepare container device: %w", err)
	}
	if err := runStep(ctx, runner, paths.mountContainer); err != nil {
		return fmt.Errorf("mount container root filesystem: %w", err)
	}
	containerMounted = true
	if err := milestone(EventContainerMounted); err != nil {
		return fmt.Errorf("report container mount: %w", err)
	}

	if assignment.Workspace.Enabled {
		if err := runStep(ctx, runner, paths.waitWorkspaceNetwork); err != nil {
			return fmt.Errorf("wait for workspace network: %w", err)
		}
		if err := runStep(ctx, runner, paths.mountWorkspace); err != nil {
			return fmt.Errorf("mount workspace: %w", err)
		}
		workspaceMounted = true
		if err := runStep(ctx, runner, paths.waitWorkspace); err != nil {
			return fmt.Errorf("verify workspace mount: %w", err)
		}
		if err := milestone(EventWorkspaceMounted); err != nil {
			return fmt.Errorf("report workspace mount: %w", err)
		}
	}

	if err := runner.Run(ctx, paths.systemctl, "start", paths.workloadUnit); err != nil {
		return fmt.Errorf("start assigned workload: %w", err)
	}
	workloadStarted = true
	if err := milestone(EventWorkloadStarted); err != nil {
		return fmt.Errorf("report workload start: %w", err)
	}
	if sdkWorkload {
		if err := milestone(EventSDKListenerReady); err != nil {
			return fmt.Errorf("report SDK listener readiness: %w", err)
		}
		if err := bridge.Wait(ctx); err != nil {
			return fmt.Errorf("serve SDK gateway bridge: %w", err)
		}
	}
	return nil
}

func setEnvironment(environment []string, name, value string) []string {
	prefix := name + "="
	updated := append([]string(nil), environment...)
	for index, variable := range updated {
		if strings.HasPrefix(variable, prefix) {
			updated[index] = prefix + value
			return updated
		}
	}
	return append(updated, prefix+value)
}

func configureNetwork(
	ctx context.Context,
	network Network,
	runner commandRunner,
	ipPath string,
) error {
	commands := [][]string{
		{"link", "set", "eth0", "up"},
		{"-4", "address", "flush", "dev", "eth0", "scope", "global"},
		{"address", "add", fmt.Sprintf("%s/%d", network.Address, network.Prefix), "dev", "eth0"},
		{"route", "replace", "default", "via", network.Gateway, "dev", "eth0"},
	}
	for _, arguments := range commands {
		if err := runner.Run(ctx, ipPath, arguments...); err != nil {
			return fmt.Errorf("configure guest network with %q: %w", arguments, err)
		}
	}
	return nil
}

func installConfiguration(assignment Assignment, paths runtimePaths) error {
	workload, err := json.Marshal(assignment.Workload)
	if err != nil {
		return fmt.Errorf("encode workload configuration: %w", err)
	}
	workload = append(workload, '\n')
	if err := createAtomic(paths.workloadConfig, workload); err != nil {
		return fmt.Errorf("install workload configuration: %w", err)
	}
	if !assignment.Workspace.Enabled {
		return nil
	}

	if err := os.Mkdir(paths.workspaceConfig, 0o700); err != nil {
		return fmt.Errorf("create workspace configuration: %w", err)
	}
	values := []struct {
		name  string
		value string
	}{
		{name: "workspace-id", value: assignment.Workspace.ID},
		{name: "client-id", value: assignment.AssignmentID},
		{name: "server", value: assignment.Workspace.Server},
		{name: "export-path", value: assignment.Workspace.ExportPath},
	}
	for _, value := range values {
		if err := createAtomic(
			filepath.Join(paths.workspaceConfig, value.name),
			[]byte(value.value+"\n"),
		); err != nil {
			return fmt.Errorf("install workspace %s: %w", value.name, err)
		}
	}
	return nil
}

func createAtomic(path string, contents []byte) (err error) {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := file.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	if _, err := file.Write(contents); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	return file.Chmod(0o600)
}

func runStep(ctx context.Context, runner commandRunner, path string) error {
	if path == "" {
		return errors.New("command path is required")
	}
	return runner.Run(ctx, path)
}
