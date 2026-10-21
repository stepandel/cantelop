package nfs

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceMountPolicy(t *testing.T) {
	good := `41 20 0:99 / /workspaces rw,nosuid shared:1 - fuse.juicefs-workspace-v1 JuiceFS:workspace rw,user_id=0,group_id=0,default_permissions,allow_other` + "\n"
	for _, tt := range []struct {
		name, root, table string
		ok                bool
	}{
		{"protected", "/workspaces", good, true},
		{"ordinary JuiceFS", "/workspaces", strings.ReplaceAll(good, WorkspaceFilesystem, "fuse.juicefs"), false},
		{"subdirectory mount", "/workspaces", strings.Replace(good, " / /workspaces ", " /subdir /workspaces ", 1), false},
		{"parent only", "/workspaces/child", good, false},
		{"stacked mounts", "/workspaces", good + good, false},
		{"permissions disabled", "/workspaces", strings.Replace(good, ",default_permissions", "", 1), false},
		{"other users denied", "/workspaces", strings.Replace(good, ",allow_other", "", 1), false},
		{"untrusted owner", "/workspaces", strings.Replace(good, "user_id=0", "user_id=10001", 1), false},
		{"escaped path", "/work spaces", strings.Replace(good, "/workspaces", `/work\040spaces`, 1), true},
		{"malformed", "/workspaces", "bad entry\n", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := checkWorkspaceMount(tt.root, strings.NewReader(tt.table))
			if (err == nil) != tt.ok {
				t.Fatalf("got %v, want success=%v", err, tt.ok)
			}
		})
	}
}

func TestExportRejectsUnprotectedMountBeforeMutation(t *testing.T) {
	root := t.TempDir()
	runner := &scriptedRunner{}
	manager := testManager(t, root, runner)
	manager.checkMount = func(string) error { return ErrWorkspacePolicy }
	manager.chown = func(string, int, int) error { t.Fatal("must not chown unprotected storage"); return nil }
	_, err := manager.Export(context.Background(), "wsp_test", netip.MustParseAddr("172.31.0.2"), 10001, 10001)
	if !errors.Is(err, ErrWorkspacePolicy) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "wsp_test")); !os.IsNotExist(err) {
		t.Fatalf("created workspace before checking policy: %v", err)
	}
	for _, call := range runner.calls {
		if call.name == "/usr/sbin/exportfs" {
			t.Fatal("exported unprotected mount")
		}
	}
}
