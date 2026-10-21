package cgroup

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestManagerLifecycleAndUsage(t *testing.T) {
	root := t.TempDir()
	current := filepath.Join(root, "self.cgroup")
	if err := os.WriteFile(current, []byte("0::/system.slice/fire-fuse.service\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mountpoint := filepath.Join(root, "cgroup")
	serviceDirectory := filepath.Join(mountpoint, "system.slice", "fire-fuse.service")
	if err := os.MkdirAll(serviceDirectory, 0o750); err != nil {
		t.Fatal(err)
	}
	writeControllerFixtures(t, serviceDirectory, "cpu io memory pids\n")
	manager, err := NewManager(current, mountpoint)
	if err != nil {
		t.Fatal(err)
	}
	if got := manager.Parent(); got != "system.slice/fire-fuse.service/sandboxes" {
		t.Fatalf("Parent() = %q", got)
	}
	assertDelegationPrepared(t, serviceDirectory)
	assertControllersEnabled(t, filepath.Join(mountpoint, manager.Parent()))
	if err := manager.Prepare("sandbox-1"); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(mountpoint, manager.Parent(), "sandbox-1")
	for name, want := range map[string]string{"cpu.weight": sandboxCPUWeight, "cpu.max": sandboxCPUMax, "memory.max": strconv.FormatInt(sandboxMemoryMaxBytes, 10)} {
		value, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil || string(value) != want {
			t.Fatalf("%s = %q, %v; want %q", name, value, err, want)
		}
	}
	if err := os.WriteFile(filepath.Join(directory, "cpu.weight"), []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.Prepare("sandbox-1"); err != nil {
		t.Fatal(err)
	}
	if value, err := os.ReadFile(filepath.Join(directory, "cpu.weight")); err != nil || string(value) != sandboxCPUWeight {
		t.Fatalf("reapplied cpu.weight = %q, %v", value, err)
	}
	if err := os.WriteFile(filepath.Join(directory, "cpu.stat"), []byte("usage_usec 12345\nuser_usec 10000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "memory.current"), []byte("987654\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	usage, err := manager.Read("sandbox-1")
	if err != nil {
		t.Fatal(err)
	}
	if usage.CPUUsageUS != 12345 || usage.MemoryCurrentBytes != 987654 {
		t.Fatalf("Read() = %#v", usage)
	}
	if err := os.Remove(filepath.Join(directory, "cpu.stat")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(directory, "memory.current")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"cpu.weight", "cpu.max", "memory.max"} {
		if err := os.Remove(filepath.Join(directory, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := manager.Remove("sandbox-1"); err != nil {
		t.Fatal(err)
	}
}

func TestManagerPrepareFailsWhenCPUPolicyCannotBeApplied(t *testing.T) {
	root := t.TempDir()
	current := filepath.Join(root, "self.cgroup")
	if err := os.WriteFile(current, []byte("0::/service\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mountpoint := filepath.Join(root, "cgroup")
	serviceDirectory := filepath.Join(mountpoint, "service")
	if err := os.MkdirAll(serviceDirectory, 0o750); err != nil {
		t.Fatal(err)
	}
	writeControllerFixtures(t, serviceDirectory, "cpu memory\n")
	manager, err := NewManager(current, mountpoint)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(mountpoint, manager.Parent(), "sandbox-1")
	if err := os.Mkdir(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(directory, "cpu.max"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := manager.Prepare("sandbox-1"); err == nil {
		t.Fatal("Prepare() accepted unwritable CPU policy")
	}
}

func TestManagerRejectsUnsafeInputsAndMalformedUsage(t *testing.T) {
	root := t.TempDir()
	current := filepath.Join(root, "self.cgroup")
	if err := os.WriteFile(current, []byte("0::/service\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mountpoint := filepath.Join(root, "cgroup")
	serviceDirectory := filepath.Join(mountpoint, "service")
	if err := os.MkdirAll(serviceDirectory, 0o750); err != nil {
		t.Fatal(err)
	}
	writeControllerFixtures(t, serviceDirectory, "cpu memory\n")
	manager, err := NewManager(current, mountpoint)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Prepare("../escape"); !errors.Is(err, ErrInvalidHierarchy) {
		t.Fatalf("Prepare() error = %v", err)
	}
	if err := manager.Prepare("sandbox-1"); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(mountpoint, manager.Parent(), "sandbox-1")
	if err := os.WriteFile(filepath.Join(directory, "cpu.stat"), []byte("user_usec 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "memory.current"), []byte("not-a-number\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Read("sandbox-1"); !errors.Is(err, ErrInvalidUsage) {
		t.Fatalf("Read() error = %v", err)
	}
}

func TestNewManagerRejectsMissingUnifiedHierarchy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "self.cgroup")
	if err := os.WriteFile(path, []byte("2:cpu:/legacy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewManager(path, t.TempDir()); !errors.Is(err, ErrInvalidHierarchy) {
		t.Fatalf("NewManager() error = %v", err)
	}
}

func TestNewManagerRequiresCPUAndMemoryControllers(t *testing.T) {
	root := t.TempDir()
	current := filepath.Join(root, "self.cgroup")
	if err := os.WriteFile(current, []byte("0::/service\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mountpoint := filepath.Join(root, "cgroup")
	serviceDirectory := filepath.Join(mountpoint, "service")
	if err := os.MkdirAll(serviceDirectory, 0o750); err != nil {
		t.Fatal(err)
	}
	writeControllerFixtures(t, serviceDirectory, "cpu io\n")
	if _, err := NewManager(current, mountpoint); !errors.Is(err, ErrInvalidHierarchy) {
		t.Fatalf("NewManager() error = %v", err)
	}
}

func writeControllerFixtures(t *testing.T, directory, controllers string) {
	t.Helper()
	for name, content := range map[string]string{
		"cgroup.controllers":     controllers,
		"cgroup.subtree_control": "",
	} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func assertDelegationPrepared(t *testing.T, directory string) {
	t.Helper()
	process, err := os.ReadFile(filepath.Join(directory, managerCgroupName, "cgroup.procs"))
	if err != nil {
		t.Fatal(err)
	}
	if string(process) != strconv.Itoa(os.Getpid()) {
		t.Fatalf("manager cgroup process = %q", process)
	}
	assertControllersEnabled(t, directory)
}

func assertControllersEnabled(t *testing.T, directory string) {
	t.Helper()
	controllers, err := os.ReadFile(filepath.Join(directory, "cgroup.subtree_control"))
	if err != nil {
		t.Fatal(err)
	}
	if string(controllers) != "+cpu +memory" {
		t.Fatalf("subtree controllers = %q", controllers)
	}
}
