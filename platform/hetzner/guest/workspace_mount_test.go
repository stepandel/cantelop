package guest_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Exercise the actual shell traps with a fake sysfs control and mount helper.
func TestWorkspaceMountRestoresRCUPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, initial, signal   string
		mountStatus, wantStatus int
	}{
		{name: "success", initial: "0"},
		{name: "already expedited", initial: "1"},
		{name: "mount failure", initial: "0", mountStatus: 32, wantStatus: 32},
		{name: "failure while expedited", initial: "1", mountStatus: 32, wantStatus: 32},
		{name: "terminated", initial: "0", signal: "TERM", wantStatus: 143},
		{name: "interrupted", initial: "0", signal: "INT", wantStatus: 130},
		{name: "unavailable control"},
		{name: "invalid control", initial: "invalid", wantStatus: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			config := filepath.Join(root, "config")
			if err := os.Mkdir(config, 0755); err != nil {
				t.Fatal(err)
			}
			for name, value := range map[string]string{"workspace-id": "wsp_test", "client-id": "vm_test", "server": "10.0.0.1", "export-path": "/exports/wsp_test"} {
				if err := os.WriteFile(filepath.Join(config, name), []byte(value), 0600); err != nil {
					t.Fatal(err)
				}
			}
			rcu := filepath.Join(root, "rcu")
			if tc.initial != "" {
				if err := os.WriteFile(rcu, []byte(tc.initial+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			marker := filepath.Join(root, "mounted")
			helper := filepath.Join(root, "mount-helper")
			body := "#!/usr/bin/env bash\nset -eu\n"
			if tc.initial != "" {
				body += "[[ $(< '" + rcu + "') == 1 ]] || exit 99\n"
			}
			body += "touch '" + marker + "'\n"
			if tc.signal != "" {
				body += "kill -" + tc.signal + " \"$PPID\"\n"
			}
			body += "exit " + strconv.Itoa(tc.mountStatus) + "\n"
			if err := os.WriteFile(helper, []byte(body), 0700); err != nil {
				t.Fatal(err)
			}
			source, err := os.ReadFile("fire-fuse-mount-workspace")
			if err != nil {
				t.Fatal(err)
			}
			script := strings.NewReplacer(
				"/run/fire-fuse/workspace", config,
				"readonly mountpoint='/workspace'", "readonly mountpoint='"+filepath.Join(root, "workspace")+"'",
				"/sys/fs/nfs/net/nfs_client/identifier", filepath.Join(root, "client-id"),
				"/sys/kernel/rcu_expedited", rcu,
				"/usr/sbin/mount.nfs4", helper,
			).Replace(string(source))
			if err := os.WriteFile(filepath.Join(root, "client-id"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "script")
			if err := os.WriteFile(path, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			output, err := exec.Command("bash", path).CombinedOutput()
			got := 0
			if err != nil {
				exit, ok := err.(*exec.ExitError)
				if !ok {
					t.Fatal(err)
				}
				got = exit.ExitCode()
			}
			if got != tc.wantStatus {
				t.Fatalf("status = %d, want %d: %s", got, tc.wantStatus, output)
			}
			if tc.initial != "" {
				value, err := os.ReadFile(rcu)
				if err != nil {
					t.Fatal(err)
				}
				if strings.TrimSpace(string(value)) != tc.initial {
					t.Errorf("RCU policy = %q, want %q", value, tc.initial)
				}
			}
			_, err = os.Stat(marker)
			if tc.initial == "invalid" {
				if !os.IsNotExist(err) {
					t.Error("mount ran with invalid RCU policy")
				}
			} else if err != nil {
				t.Fatalf("mount did not run: %v", err)
			}
		})
	}
}
