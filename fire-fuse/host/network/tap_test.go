package network

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stepandel/cantelop/fire-fuse/telemetry/warmuptrace"
)

type commandCall struct {
	name string
	args []string
}

type scriptedRunner struct {
	calls  []commandCall
	errors []error
}

func (r *scriptedRunner) run(_ context.Context, name string, args ...string) error {
	r.calls = append(r.calls, commandCall{name: name, args: append([]string(nil), args...)})
	call := len(r.calls) - 1
	if call < len(r.errors) {
		return r.errors[call]
	}
	return nil
}

func TestTAPName(t *testing.T) {
	first, err := TAPName("vm-0123456789abcdef")
	if err != nil {
		t.Fatalf("TAPName() error = %v", err)
	}
	again, err := TAPName("vm-0123456789abcdef")
	if err != nil {
		t.Fatalf("second TAPName() error = %v", err)
	}
	second, err := TAPName("vm-fedcba9876543210")
	if err != nil {
		t.Fatalf("other TAPName() error = %v", err)
	}
	if first != again {
		t.Errorf("TAPName() is not stable: %q then %q", first, again)
	}
	if first == second {
		t.Errorf("different VM IDs produced the same TAP name %q", first)
	}
	if len(first) > linuxInterfaceNameMax || !strings.HasPrefix(first, tapNamePrefix) {
		t.Errorf("TAP name %q is not a safe fire-fuse interface name", first)
	}
}

func TestTAPNameRejectsInvalidVMID(t *testing.T) {
	for _, id := range []string{"", "../vm", "vm_with_underscore", strings.Repeat("a", 65)} {
		if _, err := TAPName(id); !errors.Is(err, ErrInvalidVMID) {
			t.Errorf("TAPName(%q) error = %v, want ErrInvalidVMID", id, err)
		}
	}
}

func TestIsManagedTAPName(t *testing.T) {
	managed, err := TAPName("vm-test")
	if err != nil {
		t.Fatalf("TAPName() error = %v", err)
	}
	for _, test := range []struct {
		name string
		want bool
	}{
		{name: managed, want: true},
		{name: "eth0"},
		{name: "ff0123456789ab"},
		{name: "ff0123456789abcd"},
		{name: "FF0123456789abc"},
		{name: "ff0123456789abg"},
	} {
		if got := IsManagedTAPName(test.name); got != test.want {
			t.Errorf("IsManagedTAPName(%q) = %t, want %t", test.name, got, test.want)
		}
	}
}

func TestTAPAlias(t *testing.T) {
	alias, err := TAPAlias("vm-test")
	if err != nil {
		t.Fatalf("TAPAlias() error = %v", err)
	}
	if alias != "fire-fuse:vm-test" {
		t.Errorf("TAPAlias() = %q, want fire-fuse:vm-test", alias)
	}
	if _, err := TAPAlias("../vm"); !errors.Is(err, ErrInvalidVMID) {
		t.Errorf("TAPAlias() error = %v, want ErrInvalidVMID", err)
	}
}

func TestValidateTAPOwnership(t *testing.T) {
	name, err := TAPName("vm-test")
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateTAPOwnership(name, "fire-fuse:vm-test"); err != nil {
		t.Errorf("ValidateTAPOwnership() error = %v", err)
	}
	for _, alias := range []string{"", "someone-else:vm-test", "fire-fuse:vm-other", "fire-fuse:../vm"} {
		if err := ValidateTAPOwnership(name, alias); !errors.Is(err, ErrInvalidTAPOwnership) {
			t.Errorf("ValidateTAPOwnership(%q) error = %v, want ErrInvalidTAPOwnership", alias, err)
		}
	}
}

func TestReadTAPAlias(t *testing.T) {
	name, err := TAPName("vm-test")
	if err != nil {
		t.Fatal(err)
	}
	classPath := t.TempDir()
	interfacePath := filepath.Join(classPath, name)
	if err := os.Mkdir(interfacePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(interfacePath, "ifalias"), []byte("fire-fuse:vm-test\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	alias, err := readTAPAliasAt(classPath, name)
	if err != nil {
		t.Fatalf("readTAPAliasAt() error = %v", err)
	}
	if alias != "fire-fuse:vm-test" {
		t.Errorf("alias = %q, want fire-fuse:vm-test", alias)
	}
	if _, err := readTAPAliasAt(classPath, "eth0"); !errors.Is(err, ErrInvalidTAPOwnership) {
		t.Errorf("readTAPAliasAt(unmanaged) error = %v, want ErrInvalidTAPOwnership", err)
	}
}

func TestTAPManagerCreateAndDelete(t *testing.T) {
	runner := &scriptedRunner{}
	manager, err := newTAPManager("/usr/sbin/ip", runner.run)
	if err != nil {
		t.Fatalf("newTAPManager() error = %v", err)
	}
	trace := warmuptrace.New()
	ctx := warmuptrace.WithContext(context.Background(), trace)
	name, err := manager.Create(ctx, "vm-test", testHostAddress(), 1000, 1001)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	manager.readAlias = func(gotName string) (string, error) {
		if gotName != name {
			t.Errorf("read alias name = %q, want %q", gotName, name)
		}
		return "fire-fuse:vm-test", nil
	}
	if err := manager.Delete(context.Background(), "vm-test"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	want := []commandCall{
		{
			name: "/usr/sbin/ip",
			args: []string{"tuntap", "add", "dev", name, "mode", "tap", "user", "1000", "group", "1001"},
		},
		{name: "/usr/sbin/ip", args: []string{"link", "set", "dev", name, "alias", "fire-fuse:vm-test"}},
		{name: "/usr/sbin/ip", args: []string{"addr", "add", "172.16.0.1/30", "dev", name}},
		{name: "/usr/sbin/ip", args: []string{"link", "set", "dev", name, "up"}},
		{name: "/usr/sbin/ip", args: []string{"link", "delete", "dev", name}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Errorf("commands = %#v, want %#v", runner.calls, want)
	}
	assertWarmupSteps(t, trace, []string{
		"network.tap.validate",
		"network.tap.create_device",
		"network.tap.set_ownership_alias",
		"network.tap.assign_host_address",
		"network.tap.bring_link_up",
	})
}

func TestTAPManagerCreatesUnaddressedSnapshotInterface(t *testing.T) {
	runner := &scriptedRunner{}
	manager, err := newTAPManager("/usr/sbin/ip", runner.run)
	if err != nil {
		t.Fatal(err)
	}
	name, err := manager.CreateUnaddressed(
		context.Background(),
		"snapshot-template",
		1000,
		1001,
	)
	if err != nil {
		t.Fatalf("CreateUnaddressed() error = %v", err)
	}
	want := []commandCall{
		{
			name: "/usr/sbin/ip",
			args: []string{
				"tuntap", "add", "dev", name, "mode", "tap",
				"user", "1000", "group", "1001",
			},
		},
		{
			name: "/usr/sbin/ip",
			args: []string{
				"link", "set", "dev", name,
				"alias", "fire-fuse:snapshot-template",
			},
		},
		{name: "/usr/sbin/ip", args: []string{"link", "set", "dev", name, "up"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("commands = %#v, want %#v", runner.calls, want)
	}
}

func assertWarmupSteps(t *testing.T, trace *warmuptrace.Trace, names []string) {
	t.Helper()
	steps := make(map[string]bool)
	for _, step := range trace.Snapshot().Steps {
		steps[step.Name] = true
	}
	for _, name := range names {
		if !steps[name] {
			t.Errorf("warmup trace is missing %q", name)
		}
	}
}

func TestTAPManagerRollsBackFailedOwnershipMarker(t *testing.T) {
	aliasErr := errors.New("alias setup failed")
	runner := &scriptedRunner{errors: []error{nil, aliasErr, nil}}
	manager, err := newTAPManager("/usr/sbin/ip", runner.run)
	if err != nil {
		t.Fatalf("newTAPManager() error = %v", err)
	}

	_, err = manager.Create(context.Background(), "vm-test", testHostAddress(), 1000, 1000)
	if !errors.Is(err, aliasErr) {
		t.Fatalf("Create() error = %v, want alias error", err)
	}
	if len(runner.calls) != 3 || runner.calls[2].args[1] != "delete" {
		t.Errorf("rollback commands = %#v, want link delete", runner.calls)
	}
}

func TestTAPManagerRollsBackFailedAddressSetup(t *testing.T) {
	addressErr := errors.New("address setup failed")
	runner := &scriptedRunner{errors: []error{nil, nil, addressErr, nil}}
	manager, err := newTAPManager("/usr/sbin/ip", runner.run)
	if err != nil {
		t.Fatalf("newTAPManager() error = %v", err)
	}

	_, err = manager.Create(context.Background(), "vm-test", testHostAddress(), 1000, 1000)
	if !errors.Is(err, addressErr) {
		t.Fatalf("Create() error = %v, want address error", err)
	}
	if len(runner.calls) != 4 || runner.calls[3].args[1] != "delete" {
		t.Errorf("rollback commands = %#v, want link delete", runner.calls)
	}
}

func TestTAPManagerRollsBackFailedLinkSetup(t *testing.T) {
	linkErr := errors.New("link setup failed")
	runner := &scriptedRunner{errors: []error{nil, nil, nil, linkErr, nil}}
	manager, err := newTAPManager("/usr/sbin/ip", runner.run)
	if err != nil {
		t.Fatalf("newTAPManager() error = %v", err)
	}

	_, err = manager.Create(context.Background(), "vm-test", testHostAddress(), 1000, 1000)
	if !errors.Is(err, linkErr) {
		t.Fatalf("Create() error = %v, want link error", err)
	}
	if len(runner.calls) != 5 || runner.calls[4].args[1] != "delete" {
		t.Errorf("rollback commands = %#v, want link delete", runner.calls)
	}
}

func TestTAPManagerReportsRollbackFailure(t *testing.T) {
	linkErr := errors.New("link setup failed")
	cleanupErr := errors.New("delete failed")
	runner := &scriptedRunner{errors: []error{nil, nil, nil, linkErr, cleanupErr}}
	manager, err := newTAPManager("/usr/sbin/ip", runner.run)
	if err != nil {
		t.Fatalf("newTAPManager() error = %v", err)
	}

	_, err = manager.Create(context.Background(), "vm-test", testHostAddress(), 1000, 1000)
	if !errors.Is(err, linkErr) || !errors.Is(err, cleanupErr) {
		t.Fatalf("Create() error = %v, want link and cleanup errors", err)
	}
}

func TestTAPManagerRollbackSurvivesRequestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	run := func(commandCtx context.Context, _ string, _ ...string) error {
		calls++
		switch calls {
		case 1:
			return nil
		case 2:
			return nil
		case 3:
			return nil
		case 4:
			cancel()
			return context.Canceled
		case 5:
			if err := commandCtx.Err(); err != nil {
				t.Errorf("rollback context error = %v, want active cleanup context", err)
			}
			return nil
		default:
			t.Fatalf("unexpected command call %d", calls)
			return nil
		}
	}
	manager, err := newTAPManager("/usr/sbin/ip", run)
	if err != nil {
		t.Fatalf("newTAPManager() error = %v", err)
	}

	if _, err := manager.Create(ctx, "vm-test", testHostAddress(), 1000, 1000); !errors.Is(err, context.Canceled) {
		t.Fatalf("Create() error = %v, want context.Canceled", err)
	}
	if calls != 5 {
		t.Errorf("command calls = %d, want 5", calls)
	}
}

func TestTAPManagerDeleteIsIdempotent(t *testing.T) {
	runner := &scriptedRunner{}
	manager, err := newTAPManager("/usr/sbin/ip", runner.run)
	if err != nil {
		t.Fatalf("newTAPManager() error = %v", err)
	}
	manager.readAlias = func(string) (string, error) { return "", os.ErrNotExist }

	if err := manager.Delete(context.Background(), "vm-test"); err != nil {
		t.Fatalf("Delete() error = %v, want nil", err)
	}
	if len(runner.calls) != 0 {
		t.Errorf("commands = %#v, want none", runner.calls)
	}
}

func TestTAPManagerDeleteRejectsMismatchedOwnership(t *testing.T) {
	runner := &scriptedRunner{}
	manager, err := newTAPManager("/usr/sbin/ip", runner.run)
	if err != nil {
		t.Fatalf("newTAPManager() error = %v", err)
	}
	manager.readAlias = func(string) (string, error) { return "someone-else:vm-test", nil }

	if err := manager.Delete(context.Background(), "vm-test"); !errors.Is(err, ErrInvalidTAPOwnership) {
		t.Fatalf("Delete() error = %v, want ErrInvalidTAPOwnership", err)
	}
	if len(runner.calls) != 0 {
		t.Errorf("commands = %#v, want none", runner.calls)
	}
}

func TestTAPManagerDeleteReportsAliasReadFailure(t *testing.T) {
	wantErr := errors.New("alias read failed")
	runner := &scriptedRunner{}
	manager, err := newTAPManager("/usr/sbin/ip", runner.run)
	if err != nil {
		t.Fatalf("newTAPManager() error = %v", err)
	}
	manager.readAlias = func(string) (string, error) { return "", wantErr }

	if err := manager.Delete(context.Background(), "vm-test"); !errors.Is(err, wantErr) {
		t.Fatalf("Delete() error = %v, want alias read failure", err)
	}
	if len(runner.calls) != 0 {
		t.Errorf("commands = %#v, want none", runner.calls)
	}
}

func TestTAPManagerDeleteToleratesDisappearanceAfterOwnershipCheck(t *testing.T) {
	runner := &scriptedRunner{errors: []error{ErrTAPNotFound}}
	manager, err := newTAPManager("/usr/sbin/ip", runner.run)
	if err != nil {
		t.Fatalf("newTAPManager() error = %v", err)
	}
	manager.readAlias = func(string) (string, error) { return "fire-fuse:vm-test", nil }

	if err := manager.Delete(context.Background(), "vm-test"); err != nil {
		t.Fatalf("Delete() error = %v, want nil", err)
	}
}

func TestTAPManagerValidatesConfigurationAndOwnership(t *testing.T) {
	if _, err := NewTAPManager("ip"); !errors.Is(err, ErrInvalidTAPConfig) {
		t.Fatalf("NewTAPManager() error = %v, want ErrInvalidTAPConfig", err)
	}
	manager, err := newTAPManager("/usr/sbin/ip", func(context.Context, string, ...string) error { return nil })
	if err != nil {
		t.Fatalf("newTAPManager() error = %v", err)
	}
	if _, err := manager.Create(context.Background(), "vm-test", testHostAddress(), 0, 1000); !errors.Is(err, ErrInvalidTAPConfig) {
		t.Fatalf("Create() error = %v, want ErrInvalidTAPConfig", err)
	}
}

func TestTAPManagerValidatesHostAddressBeforeCreatingDevice(t *testing.T) {
	runner := &scriptedRunner{}
	manager, err := newTAPManager("/usr/sbin/ip", runner.run)
	if err != nil {
		t.Fatalf("newTAPManager() error = %v", err)
	}

	for _, address := range []netip.Prefix{
		{},
		netip.MustParsePrefix("172.16.0.0/30"),
		netip.MustParsePrefix("172.16.0.1/24"),
		netip.MustParsePrefix("203.0.113.1/30"),
		netip.MustParsePrefix("fd00::1/126"),
	} {
		if _, err := manager.Create(context.Background(), "vm-test", address, 1000, 1000); !errors.Is(err, ErrInvalidTAPConfig) {
			t.Errorf("Create(hostAddress=%s) error = %v, want ErrInvalidTAPConfig", address, err)
		}
	}
	if len(runner.calls) != 0 {
		t.Errorf("commands = %#v, want none", runner.calls)
	}
}

func TestRunCommandClassifiesMissingDevice(t *testing.T) {
	err := runCommand(
		context.Background(),
		"/bin/sh",
		"-c",
		"echo 'Cannot find device ffmissing' >&2; exit 1",
	)
	if !errors.Is(err, ErrTAPNotFound) {
		t.Fatalf("runCommand() error = %v, want ErrTAPNotFound", err)
	}
}

func testHostAddress() netip.Prefix {
	return netip.MustParsePrefix("172.16.0.1/30")
}
