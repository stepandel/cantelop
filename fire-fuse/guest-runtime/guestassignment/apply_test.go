package guestassignment

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stepandel/cantelop/fire-fuse/guest-runtime/guestconfig"
)

type commandCall struct {
	name string
	args []string
}

type recordingRunner struct {
	calls  []commandCall
	failAt string
}

func acceptTestClock(int64) error { return nil }

type testBridge struct {
	waited bool
	closed bool
}

func (b *testBridge) Wait(context.Context) error {
	b.waited = true
	return nil
}

func (b *testBridge) Close() error {
	b.closed = true
	return nil
}

func acceptBridge(context.Context, string, int, int) (sdkBridge, error) {
	return &testBridge{}, nil
}

func (r *recordingRunner) Run(_ context.Context, name string, arguments ...string) error {
	r.calls = append(r.calls, commandCall{name: name, args: append([]string(nil), arguments...)})
	if name == r.failAt {
		return errors.New("injected failure")
	}
	return nil
}

func testRuntimePaths(t *testing.T) runtimePaths {
	t.Helper()
	root := t.TempDir()
	return runtimePaths{
		workloadConfig:         filepath.Join(root, "run/fire-fuse/workload.json"),
		workspaceConfig:        filepath.Join(root, "run/fire-fuse/workspace"),
		ip:                     "ip",
		prepareContainerDevice: "prepare-container",
		mountContainer:         "mount-container",
		unmountContainer:       "unmount-container",
		waitWorkspaceNetwork:   "wait-network",
		mountWorkspace:         "mount-workspace",
		waitWorkspace:          "wait-workspace",
		umount:                 "umount",
		systemctl:              "systemctl",
		workloadUnit:           "assigned.service",
	}
}

func TestApplyInstallsAssignmentAndStartsWorkload(t *testing.T) {
	paths := testRuntimePaths(t)
	runner := &recordingRunner{}
	var milestones []string
	var clockUnixNano int64
	err := apply(
		context.Background(),
		validAssignment(),
		func(event string) error {
			milestones = append(milestones, event)
			return nil
		},
		func(value int64) error {
			clockUnixNano = value
			return nil
		},
		runner,
		paths,
		acceptBridge,
	)
	if err != nil {
		t.Fatalf("apply() error = %v", err)
	}

	wantMilestones := []string{
		EventClockSynchronized,
		EventNetworkConfigured,
		EventContainerMounted,
		EventWorkspaceMounted,
		EventWorkloadStarted,
	}
	if !reflect.DeepEqual(milestones, wantMilestones) {
		t.Fatalf("milestones = %q, want %q", milestones, wantMilestones)
	}
	if clockUnixNano != validAssignment().ClockUnixNano {
		t.Fatalf("clock timestamp = %d", clockUnixNano)
	}
	wantCalls := []commandCall{
		{name: "ip", args: []string{"link", "set", "eth0", "up"}},
		{name: "ip", args: []string{"-4", "address", "flush", "dev", "eth0", "scope", "global"}},
		{name: "ip", args: []string{"address", "add", "172.16.4.10/30", "dev", "eth0"}},
		{name: "ip", args: []string{"route", "replace", "default", "via", "172.16.4.9", "dev", "eth0"}},
		{name: "prepare-container"},
		{name: "mount-container"},
		{name: "wait-network"},
		{name: "mount-workspace"},
		{name: "wait-workspace"},
		{name: "systemctl", args: []string{"start", "assigned.service"}},
	}
	if !reflect.DeepEqual(runner.calls, wantCalls) {
		t.Fatalf("commands = %#v, want %#v", runner.calls, wantCalls)
	}

	workloadFile, err := os.Open(paths.workloadConfig)
	if err != nil {
		t.Fatalf("open workload configuration: %v", err)
	}
	workload, err := guestconfig.Decode(workloadFile)
	workloadFile.Close()
	if err != nil {
		t.Fatalf("decode workload configuration: %v", err)
	}
	if !reflect.DeepEqual(workload, validAssignment().Workload) {
		t.Fatalf("workload = %#v", workload)
	}
	for name, want := range map[string]string{
		"workspace-id": "workspace-01\n",
		"client-id":    "assignment-01\n",
		"server":       "172.16.4.1\n",
		"export-path":  "/srv/fire-fuse/workspaces/workspace-01\n",
	} {
		got, err := os.ReadFile(filepath.Join(paths.workspaceConfig, name))
		if err != nil {
			t.Fatalf("read workspace %s: %v", name, err)
		}
		if string(got) != want {
			t.Errorf("workspace %s = %q, want %q", name, got, want)
		}
	}
}

func TestApplyBridgesSDKListenerBeforeStartingWorkload(t *testing.T) {
	paths := testRuntimePaths(t)
	runner := &recordingRunner{}
	assignment := validAssignment()
	assignment.AssignmentID = "sbx-11111111111111111111111111111111"
	assignment.Workload.Process.Env = append(
		assignment.Workload.Process.Env,
		guestconfig.InternalPortEnvironmentName+"=8080",
		"CANTELOP_SANDBOX_ID=stale-image-identity",
	)
	bridge := &testBridge{}
	var listenerAddress string
	var listenerPort int
	var targetPort int
	var milestones []string
	err := apply(
		context.Background(),
		assignment,
		func(event string) error {
			milestones = append(milestones, event)
			return nil
		},
		acceptTestClock,
		runner,
		paths,
		func(_ context.Context, address string, listener, target int) (sdkBridge, error) {
			listenerAddress = address
			listenerPort = listener
			targetPort = target
			return bridge, nil
		},
	)
	if err != nil {
		t.Fatalf("apply() error = %v", err)
	}
	if listenerAddress != assignment.Network.Address || listenerPort != 8080 || targetPort != sdkBridgeTargetPort {
		t.Fatalf(
			"bridge = %s:%d -> %d, want %s:%d -> %d",
			listenerAddress,
			listenerPort,
			targetPort,
			assignment.Network.Address,
			8080,
			sdkBridgeTargetPort,
		)
	}
	if !bridge.waited || !bridge.closed {
		t.Fatalf("bridge lifecycle = waited:%t closed:%t", bridge.waited, bridge.closed)
	}
	wantTail := []string{EventWorkloadStarted, EventSDKListenerReady}
	if !reflect.DeepEqual(milestones[len(milestones)-2:], wantTail) {
		t.Fatalf("milestone tail = %q, want %q", milestones[len(milestones)-2:], wantTail)
	}
	workloadFile, err := os.Open(paths.workloadConfig)
	if err != nil {
		t.Fatal(err)
	}
	workload, err := guestconfig.Decode(workloadFile)
	workloadFile.Close()
	if err != nil {
		t.Fatal(err)
	}
	identityCount := 0
	for _, entry := range workload.Process.Env {
		if strings.HasPrefix(entry, "CANTELOP_SANDBOX_ID=") {
			identityCount++
			if entry != "CANTELOP_SANDBOX_ID="+assignment.AssignmentID {
				t.Fatalf("stale sandbox identity: %s", entry)
			}
		}
	}
	if identityCount != 1 {
		t.Fatalf("sandbox identity count=%d", identityCount)
	}
	configuredPort, configured, err := guestconfig.SDKListenerPort(workload.Process.Env)
	if err != nil || !configured || configuredPort != sdkBridgeTargetPort {
		t.Fatalf("installed SDK target port = %d, %t, %v", configuredPort, configured, err)
	}
}

func TestApplySkipsDisabledWorkspace(t *testing.T) {
	paths := testRuntimePaths(t)
	runner := &recordingRunner{}
	assignment := validAssignment()
	assignment.Workload.Workspace = guestconfig.Workspace{}
	assignment.Workspace = Workspace{}
	if err := apply(context.Background(), assignment, nil, acceptTestClock, runner, paths, acceptBridge); err != nil {
		t.Fatalf("apply() error = %v", err)
	}
	for _, call := range runner.calls {
		if strings.Contains(call.name, "workspace") || call.name == "umount" {
			t.Errorf("unexpected workspace command: %#v", call)
		}
	}
	if _, err := os.Stat(paths.workspaceConfig); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace configuration exists or stat failed: %v", err)
	}
}

func TestApplyCleansMountedFilesystemsAfterFailure(t *testing.T) {
	paths := testRuntimePaths(t)
	runner := &recordingRunner{failAt: "systemctl"}
	err := apply(context.Background(), validAssignment(), nil, acceptTestClock, runner, paths, acceptBridge)
	if err == nil {
		t.Fatal("apply() error = nil")
	}
	wantTail := []commandCall{
		{name: "umount", args: []string{"/workspace"}},
		{name: "unmount-container"},
	}
	if !reflect.DeepEqual(runner.calls[len(runner.calls)-2:], wantTail) {
		t.Fatalf("cleanup commands = %#v, want %#v", runner.calls[len(runner.calls)-2:], wantTail)
	}
}

func TestApplyStopsWhenClockSynchronizationFails(t *testing.T) {
	paths := testRuntimePaths(t)
	runner := &recordingRunner{}
	err := apply(
		context.Background(),
		validAssignment(),
		nil,
		func(int64) error { return errors.New("injected clock failure") },
		runner,
		paths,
		acceptBridge,
	)
	if err == nil || !strings.Contains(err.Error(), "synchronize realtime clock") {
		t.Fatalf("apply() error = %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("commands after clock failure = %#v", runner.calls)
	}
}

func TestApplyRefusesToReplaceConfiguration(t *testing.T) {
	paths := testRuntimePaths(t)
	if err := os.MkdirAll(filepath.Dir(paths.workloadConfig), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.workloadConfig, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := apply(
		context.Background(),
		validAssignment(),
		nil,
		acceptTestClock,
		&recordingRunner{},
		paths,
		acceptBridge,
	)
	if err == nil {
		t.Fatal("apply() error = nil")
	}
	got, readErr := os.ReadFile(paths.workloadConfig)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "existing" {
		t.Fatalf("existing configuration was replaced with %q", got)
	}
}

func TestInstalledWorkloadConfigurationContainsOnlyWorkload(t *testing.T) {
	paths := testRuntimePaths(t)
	if err := installConfiguration(validAssignment(), paths); err != nil {
		t.Fatalf("installConfiguration() error = %v", err)
	}
	document, err := os.ReadFile(paths.workloadConfig)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(document, &object); err != nil {
		t.Fatal(err)
	}
	if _, exists := object["assignment_id"]; exists {
		t.Fatal("workload configuration contains assignment_id")
	}
}
