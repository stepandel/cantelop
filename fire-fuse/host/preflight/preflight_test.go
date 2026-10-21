package preflight

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stepandel/cantelop/fire-fuse/config"
	"github.com/stepandel/cantelop/fire-fuse/guest-runtime/guestconfig"
)

func TestCheck(t *testing.T) {
	cfg := hostFixture(t)
	// Insert a profile that sorts before the fixture's existing "test" profile
	// so the observed trust checks prove map iteration is normalized.
	cfg.Profiles["alpha"] = cfg.Profiles["test"]
	var ownership struct{ uid, gid int }

	var trustedFields []string
	var trustedFiles []string
	if err := check(cfg, "/dev/null", forwardingFixture(t, "1\n"), func(_ string, uid, gid int) error {
		ownership.uid = uid
		ownership.gid = gid
		return nil
	}, func(field, _ string) error {
		trustedFields = append(trustedFields, field)
		return nil
	}, func(field, _ string) error {
		trustedFiles = append(trustedFiles, field)
		return nil
	}); err != nil {
		t.Fatalf("check() error = %v", err)
	}
	if ownership.uid != cfg.JailerUID || ownership.gid != cfg.JailerGID {
		t.Errorf("ownership probe = %d:%d, want %d:%d", ownership.uid, ownership.gid, cfg.JailerUID, cfg.JailerGID)
	}
	if got := strings.Join(trustedFields, ","); got != "workspace.root,runtime_dir,state_db parent,profiles.alpha.snapshot_bundle,profiles.test.snapshot_bundle" {
		t.Errorf("trusted directory checks = %q, want workspace, runtime, and state parent", got)
	}
	wantTrustedFiles := strings.Join([]string{
		"firecracker_bin",
		"jailer_bin",
		"network.ip_bin",
		"network.iptables_bin",
		"network.ip6tables_bin",
		"workspace.exportfs_bin",
		"workspace.juicefs_bin",
		"workspace.mountpoint_bin",
		"profiles.alpha.kernel_image",
		"profiles.alpha.rootfs_image",
		"profiles.alpha.snapshot_bundle.manifest.json",
		"profiles.alpha.snapshot_bundle.vmstate",
		"profiles.alpha.snapshot_bundle.memory",
		"profiles.test.kernel_image",
		"profiles.test.rootfs_image",
		"profiles.test.snapshot_bundle.manifest.json",
		"profiles.test.snapshot_bundle.vmstate",
		"profiles.test.snapshot_bundle.memory",
	}, ",")
	if got := strings.Join(trustedFiles, ","); got != wantTrustedFiles {
		t.Errorf("trusted file checks = %q, want %q", got, wantTrustedFiles)
	}
	entries, err := os.ReadDir(cfg.RuntimeDir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("runtime directory contains write-probe residue: %v", entries)
	}
}

func TestCheckValidatesConfiguredSnapshotBundle(t *testing.T) {
	cfg := hostFixture(t)
	bundle := filepath.Join(t.TempDir(), "bundle")
	if err := os.Mkdir(bundle, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"manifest.json", "vmstate", "memory"} {
		if err := os.WriteFile(filepath.Join(bundle, name), []byte(name), 0o400); err != nil {
			t.Fatal(err)
		}
	}
	profile := cfg.Profiles["test"]
	profile.SnapshotBundle = bundle
	cfg.Profiles["test"] = profile
	var directories, files []string
	err := check(
		cfg,
		"/dev/null",
		forwardingFixture(t, "1\n"),
		func(string, int, int) error { return nil },
		func(field, _ string) error {
			directories = append(directories, field)
			return nil
		},
		func(field, _ string) error {
			files = append(files, field)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("check() error = %v", err)
	}
	if !slices.Contains(directories, "profiles.test.snapshot_bundle") {
		t.Fatalf("snapshot directory was not checked: %q", directories)
	}
	for _, name := range []string{"manifest.json", "vmstate", "memory"} {
		if !slices.Contains(files, "profiles.test.snapshot_bundle."+name) {
			t.Errorf("snapshot file %s was not checked: %q", name, files)
		}
	}
}

func TestCheckSnapshotBuilderExcludesUnpublishedRuntimeArtifacts(t *testing.T) {
	cfg := hostFixture(t)
	profile := cfg.Profiles["test"]
	profile.SnapshotBundle = filepath.Join(t.TempDir(), "not-built-yet")
	cfg.Profiles["test"] = profile
	cfg.Profiles["other"] = config.Profile{
		KernelImage:    "/missing/other-kernel",
		RootFSImage:    "/missing/other-rootfs",
		SnapshotBundle: "/missing/other-snapshot",
		BootArgs:       "console=ttyS0",
	}
	cfg.Images = map[string]config.ContainerImage{
		"sha256:" + strings.Repeat("a", 64): {
			RootFSImage:     "/missing/unpublished-image",
			RootFSSHA256:    strings.Repeat("b", 64),
			RootFSSizeBytes: 1024,
			Process:         preflightTestProcess(),
		},
	}

	var directories, files []string
	err := checkSnapshotBuilder(
		cfg,
		"test",
		"/dev/null",
		func(string, int, int) error { return nil },
		func(field, _ string) error {
			directories = append(directories, field)
			return nil
		},
		func(field, _ string) error {
			files = append(files, field)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("checkSnapshotBuilder() error = %v", err)
	}
	if got := strings.Join(directories, ","); got != "runtime_dir" {
		t.Fatalf("trusted directory checks = %q, want runtime_dir", got)
	}
	wantFiles := "firecracker_bin,jailer_bin,network.ip_bin," +
		"profiles.test.kernel_image,profiles.test.rootfs_image"
	if got := strings.Join(files, ","); got != wantFiles {
		t.Fatalf("trusted file checks = %q, want %q", got, wantFiles)
	}
}

func TestCheckValidatesCatalogImageArtifacts(t *testing.T) {
	cfg := hostFixture(t)
	imagePath := filepath.Join(t.TempDir(), "node-22.ext4")
	if err := os.WriteFile(imagePath, []byte("image"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.Images = map[string]config.ContainerImage{
		"sha256:" + strings.Repeat("a", 64): {
			RootFSImage:     imagePath,
			RootFSSHA256:    strings.Repeat("b", 64),
			RootFSSizeBytes: 5,
			Process:         preflightTestProcess(),
		},
	}
	var files []string
	err := check(
		cfg,
		"/dev/null",
		forwardingFixture(t, "1\n"),
		func(string, int, int) error { return nil },
		func(string, string) error { return nil },
		func(field, _ string) error {
			files = append(files, field)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("check() error = %v", err)
	}
	if !slices.Contains(files, "images.sha256:"+strings.Repeat("a", 64)+".rootfs_image") {
		t.Fatalf("catalog image was not checked: %q", files)
	}
}

func TestCheckRejectsCatalogImageSizeMismatch(t *testing.T) {
	cfg := hostFixture(t)
	imagePath := filepath.Join(t.TempDir(), "image.ext4")
	if err := os.WriteFile(imagePath, []byte("image"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.Images = map[string]config.ContainerImage{
		"sha256:" + strings.Repeat("a", 64): {
			RootFSImage:     imagePath,
			RootFSSHA256:    strings.Repeat("b", 64),
			RootFSSizeBytes: 6,
			Process:         preflightTestProcess(),
		},
	}
	err := check(
		cfg,
		"/dev/null",
		forwardingFixture(t, "1\n"),
		func(string, int, int) error { return nil },
		func(string, string) error { return nil },
		func(string, string) error { return nil },
	)
	if !errors.Is(err, ErrHostUnavailable) || !strings.Contains(err.Error(), "is 5, want 6") {
		t.Fatalf("check() error = %v, want catalog image size mismatch", err)
	}
}

func TestCheckReportsHostProblems(t *testing.T) {
	tests := []struct {
		name       string
		change     func(*testing.T, *config.Config)
		kvmPath    func(*config.Config) string
		chown      chownFunc
		forwarding string
		wantDetail string
	}{
		{
			name:       "IPv4 forwarding is disabled",
			forwarding: "0\n",
			wantDetail: "IPv4 forwarding is disabled",
		},
		{
			name:       "KVM is not a device",
			kvmPath:    func(cfg *config.Config) string { return cfg.Profiles["test"].KernelImage },
			wantDetail: "not a character device",
		},
		{
			name: "Firecracker is not executable",
			change: func(t *testing.T, cfg *config.Config) {
				if err := os.Chmod(cfg.FirecrackerBin, 0o600); err != nil {
					t.Fatalf("Chmod() error = %v", err)
				}
			},
			wantDetail: "no executable permission bit",
		},
		{
			name: "ip is not executable",
			change: func(t *testing.T, cfg *config.Config) {
				if err := os.Chmod(cfg.Network.IPBin, 0o600); err != nil {
					t.Fatalf("Chmod() error = %v", err)
				}
			},
			wantDetail: "network.ip_bin",
		},
		{
			name: "iptables is not executable",
			change: func(t *testing.T, cfg *config.Config) {
				if err := os.Chmod(cfg.Network.IPTablesBin, 0o600); err != nil {
					t.Fatalf("Chmod() error = %v", err)
				}
			},
			wantDetail: "network.iptables_bin",
		},
		{
			name: "ip6tables is not executable",
			change: func(t *testing.T, cfg *config.Config) {
				if err := os.Chmod(cfg.Network.IP6TablesBin, 0o600); err != nil {
					t.Fatalf("Chmod() error = %v", err)
				}
			},
			wantDetail: "network.ip6tables_bin",
		},
		{
			name:       "runtime path is not a directory",
			change:     func(_ *testing.T, cfg *config.Config) { cfg.RuntimeDir = cfg.Profiles["test"].KernelImage },
			wantDetail: "is not a directory",
		},
		{
			name: "boot image is empty",
			change: func(t *testing.T, cfg *config.Config) {
				if err := os.WriteFile(cfg.Profiles["test"].RootFSImage, nil, 0o600); err != nil {
					t.Fatalf("WriteFile() error = %v", err)
				}
			},
			wantDetail: "is empty",
		},
		{
			name:       "jail ownership cannot be set",
			chown:      func(string, int, int) error { return errors.New("operation not permitted") },
			wantDetail: "set jail ownership",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := hostFixture(t)
			if test.change != nil {
				test.change(t, &cfg)
			}
			kvmPath := "/dev/null"
			if test.kvmPath != nil {
				kvmPath = test.kvmPath(&cfg)
			}

			chown := test.chown
			if chown == nil {
				chown = func(string, int, int) error { return nil }
			}

			forwardingValue := test.forwarding
			if forwardingValue == "" {
				forwardingValue = "1\n"
			}
			err := check(
				cfg,
				kvmPath,
				forwardingFixture(t, forwardingValue),
				chown,
				trustAllDirectories,
				trustAllFiles,
			)
			if !errors.Is(err, ErrHostUnavailable) {
				t.Fatalf("check() error = %v, want ErrHostUnavailable", err)
			}
			if !strings.Contains(err.Error(), test.wantDetail) {
				t.Errorf("check() error = %v, want detail %q", err, test.wantDetail)
			}
		})
	}
}

func TestCheckAggregatesProblems(t *testing.T) {
	cfg := hostFixture(t)
	cfg.FirecrackerBin = filepath.Join(t.TempDir(), "missing-firecracker")
	cfg.JailerBin = filepath.Join(t.TempDir(), "missing-jailer")
	cfg.Network.IPBin = filepath.Join(t.TempDir(), "missing-ip")
	cfg.Network.IPTablesBin = filepath.Join(t.TempDir(), "missing-iptables")

	err := check(
		cfg,
		"/dev/null",
		forwardingFixture(t, "1\n"),
		func(string, int, int) error { return nil },
		trustAllDirectories,
		trustAllFiles,
	)
	if err == nil ||
		!strings.Contains(err.Error(), "missing-firecracker") ||
		!strings.Contains(err.Error(), "missing-jailer") ||
		!strings.Contains(err.Error(), "missing-ip") ||
		!strings.Contains(err.Error(), "missing-iptables") {
		t.Fatalf("check() error = %v, want all missing executables", err)
	}
}

func TestRuntimeTrustFailureSkipsOwnershipProbe(t *testing.T) {
	cfg := hostFixture(t)
	wantErr := errors.New("unsafe runtime hierarchy")
	chownCalled := false
	checkTrust := func(field, _ string) error {
		if field == "runtime_dir" {
			return wantErr
		}
		return nil
	}

	err := check(
		cfg,
		"/dev/null",
		forwardingFixture(t, "1\n"),
		func(string, int, int) error {
			chownCalled = true
			return nil
		},
		checkTrust,
		trustAllFiles,
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("check() error = %v, want runtime trust failure", err)
	}
	if chownCalled {
		t.Fatal("runtime ownership probe ran after trust validation failed")
	}
}

func TestCheckIPv4ForwardingReportsReadFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-ip-forward")
	if err := checkIPv4Forwarding(path); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("checkIPv4Forwarding() error = %v, want path detail", err)
	}
}

func forwardingFixture(t *testing.T, value string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ip_forward")
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatalf("WriteFile(ip_forward) error = %v", err)
	}
	return path
}

func hostFixture(t *testing.T) config.Config {
	t.Helper()
	directory := t.TempDir()
	write := func(name, contents string, mode os.FileMode) string {
		t.Helper()
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, []byte(contents), mode); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", name, err)
		}
		return path
	}
	runtimeDir := filepath.Join(directory, "runtime")
	if err := os.Mkdir(runtimeDir, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}

	snapshotDirectory := filepath.Join(t.TempDir(), "snapshot")
	if err := os.Mkdir(snapshotDirectory, 0o700); err != nil {
		t.Fatalf("Mkdir(snapshot) error = %v", err)
	}
	for _, name := range []string{"manifest.json", "vmstate", "memory"} {
		if err := os.WriteFile(filepath.Join(snapshotDirectory, name), []byte(name), 0o600); err != nil {
			t.Fatalf("WriteFile(snapshot %s) error = %v", name, err)
		}
	}

	return config.Config{
		FirecrackerBin:          write("firecracker", "binary", 0o700),
		JailerBin:               write("jailer", "binary", 0o700),
		JailerUID:               1000,
		JailerGID:               1000,
		RuntimeDir:              runtimeDir,
		StateDB:                 filepath.Join(runtimeDir, "state.db"),
		ConfigEncryptionKeyFile: filepath.Join(runtimeDir, "config-encryption.key"),
		Network: config.NetworkConfig{
			IPBin:        write("ip", "binary", 0o700),
			IPTablesBin:  write("iptables", "binary", 0o700),
			IP6TablesBin: write("ip6tables", "binary", 0o700),
			AddressPool:  "172.16.0.0/16",
		},
		Workspace: &config.WorkspaceConfig{
			ExportFSBin:   write("exportfs", "binary", 0o700),
			JuiceFSBin:    write("juicefs", "binary", 0o700),
			MountpointBin: write("mountpoint", "binary", 0o700),
			Root:          runtimeDir,
		},
		Profiles: map[string]config.Profile{
			"test": {
				KernelImage:    write("vmlinux", "kernel", 0o600),
				RootFSImage:    write("rootfs.ext4", "rootfs", 0o600),
				SnapshotBundle: snapshotDirectory,
				BootArgs:       "console=ttyS0",
			},
		},
	}
}

func preflightTestProcess() guestconfig.Process {
	return guestconfig.Process{
		Args:             []string{"/bin/server"},
		WorkingDirectory: "/",
	}
}

func trustAllDirectories(string, string) error {
	return nil
}

func trustAllFiles(string, string) error {
	return nil
}
