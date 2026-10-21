package nfs

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stepandel/cantelop/fire-fuse/runtime/model"
	"github.com/stepandel/cantelop/fire-fuse/telemetry/warmuptrace"
)

type commandCall struct {
	name string
	args []string
}

type scriptedRunner struct {
	calls   []commandCall
	outputs []string
	errors  []error
}

func (r *scriptedRunner) run(_ context.Context, name string, args ...string) (string, error) {
	r.calls = append(r.calls, commandCall{name: name, args: append([]string(nil), args...)})
	index := len(r.calls) - 1
	var output string
	if index < len(r.outputs) {
		output = r.outputs[index]
	}
	if index < len(r.errors) {
		return output, r.errors[index]
	}
	return output, nil
}

func TestManagerExportsExactWorkspace(t *testing.T) {
	root := t.TempDir()
	runner := &scriptedRunner{}
	manager := testManager(t, root, runner)
	guestIP := netip.MustParseAddr("172.31.0.2")

	trace := warmuptrace.New()
	ctx := warmuptrace.WithContext(context.Background(), trace)
	var ownedUID, ownedGID int
	manager.chown = func(_ string, uid, gid int) error {
		ownedUID, ownedGID = uid, gid
		return nil
	}
	workspacePath, err := manager.Export(ctx, "wsp_workspace_1", guestIP, 1000, 1001)
	if err != nil {
		t.Fatalf("Export() error = %v", err)
	}
	wantPath := filepath.Join(root, "wsp_workspace_1")
	if workspacePath != wantPath {
		t.Errorf("workspace path = %q, want %q", workspacePath, wantPath)
	}
	info, err := os.Lstat(wantPath)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Errorf("workspace mode = %v, want directory 0700", info.Mode())
	}
	if ownedUID != 1000 || ownedGID != 1001 {
		t.Errorf("workspace ownership = %d:%d, want 1000:1001", ownedUID, ownedGID)
	}

	wantCalls := []commandCall{
		{name: "/usr/bin/mountpoint", args: []string{"--quiet", root}},
		{
			name: "/usr/sbin/exportfs",
			args: []string{
				"-i",
				"-o",
				"rw,async,subtree_check,all_squash,anonuid=1000,anongid=1001,fsid=b5cfe7a0-8b75-b5dd-83d5-d2527ac19bd9",
				"172.31.0.2:" + wantPath,
			},
		},
	}
	if !reflect.DeepEqual(runner.calls, wantCalls) {
		t.Errorf("commands = %#v, want %#v", runner.calls, wantCalls)
	}
	traceSteps := make(map[string]bool)
	for _, step := range trace.Snapshot().Steps {
		traceSteps[step.Name] = true
	}
	for _, name := range []string{
		"workspace.nfs.derive_export_path",
		"workspace.nfs.validate_workspace_root",
		"workspace.nfs.verify_backing_mount",
		"workspace.nfs.create_workspace_directory",
		"workspace.nfs.publish_export",
	} {
		if !traceSteps[name] {
			t.Errorf("warmup trace is missing %q", name)
		}
	}
}

func TestWorkspaceFSIDIsStableAndWorkspaceSpecific(t *testing.T) {
	const want = "fsid=b5cfe7a0-8b75-b5dd-83d5-d2527ac19bd9"
	if got := workspaceFSIDOption("wsp_workspace_1"); got != want {
		t.Errorf("workspaceFSIDOption() = %q, want %q", got, want)
	}
	if workspaceFSIDOption("wsp_workspace_1") == workspaceFSIDOption("wsp_workspace_2") {
		t.Error("different workspace IDs produced the same NFS fsid")
	}
}

func TestManagerPurgesWorkspaceThroughJuiceFS(t *testing.T) {
	root := t.TempDir()
	workspaceID := "wsp_0123456789abcdef0123456789abcdef"
	workspacePath := filepath.Join(root, workspaceID)
	if err := os.Mkdir(workspacePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspacePath, "data"), []byte("durable"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &scriptedRunner{}
	manager := testManager(t, root, runner)
	manager.run = func(ctx context.Context, name string, args ...string) (string, error) {
		result, err := runner.run(ctx, name, args...)
		if name == "/usr/local/bin/juicefs" && err == nil {
			err = os.RemoveAll(filepath.Join(root, filepath.Base(args[len(args)-1])))
		}
		return result, err
	}

	if err := manager.Purge(context.Background(), workspaceID); err != nil {
		t.Fatalf("Purge() error = %v", err)
	}
	if _, err := os.Lstat(workspacePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Workspace remains after purge: %v", err)
	}
	want := []commandCall{
		{name: "/usr/bin/mountpoint", args: []string{"--quiet", root}},
		{name: "/usr/bin/mountpoint", args: []string{"--quiet", manager.adminRoot}},
		{name: "/usr/local/bin/juicefs", args: []string{"rmr", "--skip-trash", filepath.Join(manager.adminRoot, ".purging-"+workspaceID)}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("commands = %#v, want %#v", runner.calls, want)
	}
	if err := manager.Purge(context.Background(), workspaceID); err != nil {
		t.Fatalf("idempotent Purge() error = %v", err)
	}
}

func TestManagerResumesQuarantinedWorkspacePurge(t *testing.T) {
	root := t.TempDir()
	workspaceID := "wsp_0123456789abcdef0123456789abcdef"
	quarantinePath := filepath.Join(root, ".purging-"+workspaceID)
	if err := os.MkdirAll(quarantinePath, 0o700); err != nil {
		t.Fatal(err)
	}
	runner := &scriptedRunner{}
	manager := testManager(t, root, runner)
	manager.run = func(ctx context.Context, name string, args ...string) (string, error) {
		result, err := runner.run(ctx, name, args...)
		if name == "/usr/local/bin/juicefs" && err == nil {
			err = os.RemoveAll(filepath.Join(root, filepath.Base(args[len(args)-1])))
		}
		return result, err
	}
	if err := manager.Purge(context.Background(), workspaceID); err != nil {
		t.Fatalf("Purge() error = %v", err)
	}
	if _, err := os.Lstat(quarantinePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("quarantined Workspace remains after purge: %v", err)
	}
}

func TestManagerReusesExistingWorkspaceDirectory(t *testing.T) {
	root := t.TempDir()
	workspacePath := filepath.Join(root, "wsp_workspace_1")
	if err := os.Mkdir(workspacePath, 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &scriptedRunner{}
	manager := testManager(t, root, runner)

	got, err := manager.Export(
		context.Background(),
		"wsp_workspace_1",
		netip.MustParseAddr("172.31.0.2"),
		1000, 1000,
	)
	if err != nil {
		t.Fatalf("Export() error = %v", err)
	}
	if got != workspacePath {
		t.Errorf("workspace path = %q, want %q", got, workspacePath)
	}
}

func TestManagerRequiresMountedWorkspaceRoot(t *testing.T) {
	root := t.TempDir()
	mountErr := errors.New("not a mountpoint")
	runner := &scriptedRunner{errors: []error{mountErr}}
	manager := testManager(t, root, runner)

	_, err := manager.Export(
		context.Background(),
		"wsp_workspace_1",
		netip.MustParseAddr("172.31.0.2"),
		1000, 1000,
	)
	if !errors.Is(err, ErrWorkspaceUnmounted) || !errors.Is(err, mountErr) {
		t.Fatalf("Export() error = %v, want mount error", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "wsp_workspace_1")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace created without mounted root: %v", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("commands = %#v, want mountpoint only", runner.calls)
	}
}

func TestManagerRejectsUnsafeDirectories(t *testing.T) {
	t.Run("root symlink", func(t *testing.T) {
		parent := t.TempDir()
		target := filepath.Join(parent, "target")
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
		root := filepath.Join(parent, "root")
		if err := os.Symlink(target, root); err != nil {
			t.Fatal(err)
		}
		manager := testManager(t, root, &scriptedRunner{})
		if _, err := manager.Export(
			context.Background(),
			"wsp_workspace_1",
			netip.MustParseAddr("172.31.0.2"),
			1000, 1000,
		); !errors.Is(err, ErrWorkspaceRoot) {
			t.Fatalf("Export() error = %v, want ErrWorkspaceRoot", err)
		}
	})

	t.Run("workspace symlink", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Symlink(
			t.TempDir(),
			filepath.Join(root, "wsp_workspace_1"),
		); err != nil {
			t.Fatal(err)
		}
		manager := testManager(t, root, &scriptedRunner{})
		if _, err := manager.Export(
			context.Background(),
			"wsp_workspace_1",
			netip.MustParseAddr("172.31.0.2"),
			1000, 1000,
		); !errors.Is(err, ErrWorkspacePath) {
			t.Fatalf("Export() error = %v, want ErrWorkspacePath", err)
		}
	})
}

func TestManagerValidatesWorkspaceAndGuest(t *testing.T) {
	root := t.TempDir()
	manager := testManager(t, root, &scriptedRunner{})
	for _, test := range []struct {
		name        string
		workspaceID string
		guestIP     netip.Addr
		wantErr     error
	}{
		{
			name:        "unsafe workspace",
			workspaceID: "../workspace",
			guestIP:     netip.MustParseAddr("172.31.0.2"),
			wantErr:     model.ErrInvalidWorkspaceID,
		},
		{
			name:        "public address",
			workspaceID: "wsp_workspace_1",
			guestIP:     netip.MustParseAddr("192.0.2.2"),
			wantErr:     ErrInvalidGuestIP,
		},
		{
			name:        "IPv6 address",
			workspaceID: "wsp_workspace_1",
			guestIP:     netip.MustParseAddr("fd00::2"),
			wantErr:     ErrInvalidGuestIP,
		},
		{
			name:        "invalid address",
			workspaceID: "wsp_workspace_1",
			wantErr:     ErrInvalidGuestIP,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := manager.Export(
				context.Background(),
				test.workspaceID,
				test.guestIP,
				1000, 1000,
			); !errors.Is(err, test.wantErr) {
				t.Fatalf("Export() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestManagerReportsExportFailure(t *testing.T) {
	root := t.TempDir()
	exportErr := errors.New("export rejected")
	runner := &scriptedRunner{errors: []error{nil, exportErr}}
	manager := testManager(t, root, runner)

	if _, err := manager.Export(
		context.Background(),
		"wsp_workspace_1",
		netip.MustParseAddr("172.31.0.2"),
		1000, 1000,
	); !errors.Is(err, exportErr) {
		t.Fatalf("Export() error = %v, want %v", err, exportErr)
	}
}

func TestManagerUnexportsExactClientWithoutDeletingWorkspace(t *testing.T) {
	root := t.TempDir()
	workspacePath := filepath.Join(root, "wsp_workspace_1")
	if err := os.Mkdir(workspacePath, 0o700); err != nil {
		t.Fatal(err)
	}
	runner := &scriptedRunner{}
	manager := testManager(t, root, runner)

	if err := manager.Unexport(
		context.Background(),
		"wsp_workspace_1",
		netip.MustParseAddr("172.31.0.2"),
	); err != nil {
		t.Fatalf("Unexport() error = %v", err)
	}
	want := []commandCall{{
		name: "/usr/sbin/exportfs",
		args: []string{"-u", "172.31.0.2:" + workspacePath},
	}}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Errorf("commands = %#v, want %#v", runner.calls, want)
	}
	if _, err := os.Lstat(workspacePath); err != nil {
		t.Fatalf("persistent workspace removed: %v", err)
	}
}

func TestManagerUnexportIsIdempotent(t *testing.T) {
	root := t.TempDir()
	runner := &scriptedRunner{errors: []error{ErrExportNotFound}}
	manager := testManager(t, root, runner)
	if err := manager.Unexport(
		context.Background(),
		"wsp_workspace_1",
		netip.MustParseAddr("172.31.0.2"),
	); err != nil {
		t.Fatalf("Unexport() error = %v, want nil", err)
	}
}

func TestNewManagerValidatesConfiguration(t *testing.T) {
	for _, test := range []struct {
		name       string
		exportfs   string
		juicefs    string
		mountpoint string
		root       string
		adminRoot  string
	}{
		{name: "relative exportfs", exportfs: "exportfs", juicefs: "/bin/juicefs", mountpoint: "/bin/mountpoint", root: "/workspaces", adminRoot: "/admin"},
		{name: "relative juicefs", exportfs: "/sbin/exportfs", juicefs: "juicefs", mountpoint: "/bin/mountpoint", root: "/workspaces", adminRoot: "/admin"},
		{name: "relative mountpoint", exportfs: "/sbin/exportfs", juicefs: "/bin/juicefs", mountpoint: "mountpoint", root: "/workspaces", adminRoot: "/admin"},
		{name: "relative root", exportfs: "/sbin/exportfs", juicefs: "/bin/juicefs", mountpoint: "/bin/mountpoint", root: "workspaces", adminRoot: "/admin"},
		{name: "relative admin root", exportfs: "/sbin/exportfs", juicefs: "/bin/juicefs", mountpoint: "/bin/mountpoint", root: "/workspaces", adminRoot: "admin"},
		{name: "same roots", exportfs: "/sbin/exportfs", juicefs: "/bin/juicefs", mountpoint: "/bin/mountpoint", root: "/workspaces", adminRoot: "/workspaces"},
		{name: "unclean root", exportfs: "/sbin/exportfs", juicefs: "/bin/juicefs", mountpoint: "/bin/mountpoint", root: "/workspaces/../other", adminRoot: "/admin"},
		{name: "root filesystem", exportfs: "/sbin/exportfs", juicefs: "/bin/juicefs", mountpoint: "/bin/mountpoint", root: "/", adminRoot: "/admin"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewManager(
				test.exportfs,
				test.juicefs,
				test.mountpoint,
				test.root,
				test.adminRoot,
			); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("NewManager() error = %v, want ErrInvalidConfig", err)
			}
		})
	}
	if _, err := newManager(
		"/sbin/exportfs",
		"/bin/juicefs",
		"/bin/mountpoint",
		"/workspaces",
		"/admin",
		nil,
	); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("newManager(nil runner) error = %v, want ErrInvalidConfig", err)
	}
}

func TestRunCommandClassifiesMissingExport(t *testing.T) {
	_, err := runCommand(
		context.Background(),
		"/bin/sh",
		"-c",
		"printf 'exportfs: Could not find export to unexport.\\n' >&2; exit 1",
	)
	if !errors.Is(err, ErrExportNotFound) {
		t.Fatalf("runCommand() error = %v, want ErrExportNotFound", err)
	}
}

func testManager(t *testing.T, root string, runner *scriptedRunner) *Manager {
	t.Helper()
	manager, err := newManager(
		"/usr/sbin/exportfs",
		"/usr/local/bin/juicefs",
		"/usr/bin/mountpoint",
		root,
		t.TempDir(),
		runner.run,
	)
	if err != nil {
		t.Fatal(err)
	}
	manager.chown = func(string, int, int) error { return nil }
	manager.checkMount = func(string) error { return nil }
	return manager
}

func TestManagerRejectsRootStorageIdentity(t *testing.T) {
	for _, ids := range [][2]uint32{{0, 10001}, {10001, 0}} {
		runner := &scriptedRunner{}
		manager := testManager(t, t.TempDir(), runner)
		_, err := manager.Export(context.Background(), "wsp_test", netip.MustParseAddr("172.31.0.2"), ids[0], ids[1])
		if !errors.Is(err, ErrInvalidConfig) || len(runner.calls) != 0 {
			t.Fatalf("root mapping must fail before any side effect: %v, %#v", err, runner.calls)
		}
	}
}

func TestManagedSessionSquashPreservesIdentity(t *testing.T) {
	runner := &scriptedRunner{}
	manager := testManager(t, t.TempDir(), runner)
	manager.chown = func(_ string, uid, gid int) error {
		if uid != 10001 || gid != 10001 {
			t.Fatalf("ownership changed to %d:%d", uid, gid)
		}
		return nil
	}
	if _, err := manager.Export(context.Background(), "wsp_session", netip.MustParseAddr("172.31.0.2"), 10001, 10001); err != nil {
		t.Fatal(err)
	}
	if got := runner.calls[len(runner.calls)-1].args[2]; !strings.Contains(got, "all_squash,anonuid=10001,anongid=10001,") {
		t.Fatal(got)
	}
}
