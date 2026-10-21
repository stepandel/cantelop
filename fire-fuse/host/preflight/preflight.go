// Package preflight verifies that a configured host can run Firecracker.
package preflight

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/stepandel/cantelop/fire-fuse/config"
	"github.com/stepandel/cantelop/fire-fuse/host/hostpath"
	"github.com/stepandel/cantelop/fire-fuse/host/snapshotbundle"
)

const (
	kvmDevicePath      = "/dev/kvm"
	ipv4ForwardingPath = "/proc/sys/net/ipv4/ip_forward"
)

var ErrHostUnavailable = errors.New("firecracker host is unavailable")

type chownFunc func(string, int, int) error
type trustedDirectoryChecker func(string, string) error
type trustedFileChecker func(string, string) error

// Check verifies host resources without starting a microVM.
func Check(cfg config.Config) error {
	return check(
		cfg,
		kvmDevicePath,
		ipv4ForwardingPath,
		os.Chown,
		hostpath.CheckTrustedDirectory,
		hostpath.CheckTrustedFile,
	)
}

// CheckSnapshotBuilder verifies only the host resources needed to create a
// snapshot. The destination bundle and workload image catalog are deliberately
// excluded because the builder must work before either is published live.
func CheckSnapshotBuilder(cfg config.Config, profileName string) error {
	return checkSnapshotBuilder(
		cfg,
		profileName,
		kvmDevicePath,
		os.Chown,
		hostpath.CheckTrustedDirectory,
		hostpath.CheckTrustedFile,
	)
}

func checkSnapshotBuilder(
	cfg config.Config,
	profileName, kvmPath string,
	chown chownFunc,
	checkTrustedDirectory trustedDirectoryChecker,
	checkTrustedFile trustedFileChecker,
) error {
	profile, err := cfg.GetProfile(profileName)
	if err != nil {
		return err
	}
	checks := []error{
		checkKVM(kvmPath),
		checkExecutable("firecracker_bin", cfg.FirecrackerBin, checkTrustedFile),
		checkExecutable("jailer_bin", cfg.JailerBin, checkTrustedFile),
		checkExecutable("network.ip_bin", cfg.Network.IPBin, checkTrustedFile),
		checkRuntimeDirectory(
			cfg.RuntimeDir,
			cfg.JailerUID,
			cfg.JailerGID,
			chown,
			checkTrustedDirectory,
		),
		checkReadableNonEmptyFile(
			"profiles."+profileName+".kernel_image",
			profile.KernelImage,
			checkTrustedFile,
		),
		checkReadableNonEmptyFile(
			"profiles."+profileName+".rootfs_image",
			profile.RootFSImage,
			checkTrustedFile,
		),
	}
	if err := errors.Join(checks...); err != nil {
		return errors.Join(ErrHostUnavailable, err)
	}
	return nil
}

func check(
	cfg config.Config,
	kvmPath, forwardingPath string,
	chown chownFunc,
	checkTrustedDirectory trustedDirectoryChecker,
	checkTrustedFile trustedFileChecker,
) error {
	var checks []error
	checks = append(checks, checkKVM(kvmPath))
	checks = append(checks, checkIPv4Forwarding(forwardingPath))
	checks = append(checks, checkExecutable("firecracker_bin", cfg.FirecrackerBin, checkTrustedFile))
	checks = append(checks, checkExecutable("jailer_bin", cfg.JailerBin, checkTrustedFile))
	checks = append(checks, checkExecutable("network.ip_bin", cfg.Network.IPBin, checkTrustedFile))
	checks = append(checks, checkExecutable("network.iptables_bin", cfg.Network.IPTablesBin, checkTrustedFile))
	checks = append(checks, checkExecutable("network.ip6tables_bin", cfg.Network.IP6TablesBin, checkTrustedFile))
	if cfg.Workspace != nil {
		checks = append(checks, checkExecutable(
			"workspace.exportfs_bin",
			cfg.Workspace.ExportFSBin,
			checkTrustedFile,
		))
		checks = append(checks, checkExecutable(
			"workspace.juicefs_bin",
			cfg.Workspace.JuiceFSBin,
			checkTrustedFile,
		))
		checks = append(checks, checkExecutable(
			"workspace.mountpoint_bin",
			cfg.Workspace.MountpointBin,
			checkTrustedFile,
		))
		checks = append(checks, checkTrustedDirectory(
			"workspace.root",
			cfg.Workspace.Root,
		))
	}
	checks = append(checks, checkRuntimeDirectory(
		cfg.RuntimeDir,
		cfg.JailerUID,
		cfg.JailerGID,
		chown,
		checkTrustedDirectory,
	))
	checks = append(checks, checkTrustedDirectory("state_db parent", filepath.Dir(cfg.StateDB)))

	profileNames := make([]string, 0, len(cfg.Profiles))
	for name := range cfg.Profiles {
		profileNames = append(profileNames, name)
	}
	sort.Strings(profileNames)
	for _, name := range profileNames {
		profile := cfg.Profiles[name]
		checks = append(checks, checkReadableNonEmptyFile(
			"profiles."+name+".kernel_image",
			profile.KernelImage,
			checkTrustedFile,
		))
		checks = append(checks, checkReadableNonEmptyFile(
			"profiles."+name+".rootfs_image",
			profile.RootFSImage,
			checkTrustedFile,
		))
		checks = append(checks, checkTrustedDirectory(
			"profiles."+name+".snapshot_bundle",
			profile.SnapshotBundle,
		))
		for _, file := range []string{
			snapshotbundle.ManifestName,
			snapshotbundle.StateName,
			snapshotbundle.MemoryName,
		} {
			checks = append(checks, checkReadableNonEmptyFile(
				"profiles."+name+".snapshot_bundle."+file,
				filepath.Join(profile.SnapshotBundle, file),
				checkTrustedFile,
			))
		}
	}

	imageNames := make([]string, 0, len(cfg.Images))
	for name := range cfg.Images {
		imageNames = append(imageNames, name)
	}
	sort.Strings(imageNames)
	for _, name := range imageNames {
		image := cfg.Images[name]
		checks = append(checks, checkReadableNonEmptyFile(
			"images."+name+".rootfs_image",
			image.RootFSImage,
			checkTrustedFile,
		))
		checks = append(checks, checkFileSize(
			"images."+name+".rootfs_size_bytes",
			image.RootFSImage,
			image.RootFSSizeBytes,
		))
	}

	if err := errors.Join(checks...); err != nil {
		return errors.Join(ErrHostUnavailable, err)
	}
	return nil
}

func checkFileSize(field, path string, expected int64) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("inspect %s %s: %w", field, path, err)
	}
	if info.Size() != expected {
		return fmt.Errorf(
			"%s for %s is %d, want %d",
			field,
			path,
			info.Size(),
			expected,
		)
	}
	return nil
}

func checkIPv4Forwarding(path string) error {
	value, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read IPv4 forwarding setting %s: %w", path, err)
	}
	trimmed := strings.TrimSpace(string(value))
	if trimmed != "1" {
		return fmt.Errorf("IPv4 forwarding is disabled in %s: got %q, want %q", path, trimmed, "1")
	}
	return nil
}

func checkKVM(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("inspect KVM device %s: %w", path, err)
	}
	if info.Mode()&os.ModeCharDevice == 0 {
		return fmt.Errorf("KVM device %s is not a character device", path)
	}

	device, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open KVM device %s read-write: %w", path, err)
	}
	if err := device.Close(); err != nil {
		return fmt.Errorf("close KVM device %s: %w", path, err)
	}
	return nil
}

func checkExecutable(field, path string, checkTrustedFile trustedFileChecker) error {
	if err := checkTrustedFile(field, path); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("inspect %s %s: %w", field, path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s %s is not a regular file", field, path)
	}
	if info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("%s %s has no executable permission bit", field, path)
	}
	return checkReadable(field, path)
}

func checkReadableNonEmptyFile(field, path string, checkTrustedFile trustedFileChecker) error {
	if err := checkTrustedFile(field, path); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("inspect %s %s: %w", field, path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s %s is not a regular file", field, path)
	}
	if info.Size() == 0 {
		return fmt.Errorf("%s %s is empty", field, path)
	}
	return checkReadable(field, path)
}

func checkReadable(field, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s %s for reading: %w", field, path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close %s %s: %w", field, path, err)
	}
	return nil
}

func checkRuntimeDirectory(
	path string,
	uid, gid int,
	chown chownFunc,
	checkTrustedDirectory trustedDirectoryChecker,
) error {
	if err := checkTrustedDirectory("runtime_dir", path); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("inspect runtime_dir %s: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("runtime_dir %s is not a directory", path)
	}

	probe, err := os.CreateTemp(path, ".fire-fuse-preflight-*")
	if err != nil {
		return fmt.Errorf("create a file in runtime_dir %s: %w", path, err)
	}
	probePath := probe.Name()
	closeErr := wrapPathError("close runtime write probe", probePath, probe.Close())
	var ownershipErr error
	if closeErr == nil {
		ownershipErr = wrapPathError(
			fmt.Sprintf("set jail ownership to %d:%d on probe", uid, gid),
			probePath,
			chown(probePath, uid, gid),
		)
	}
	return errors.Join(
		closeErr,
		ownershipErr,
		wrapPathError("remove runtime write probe", probePath, os.Remove(probePath)),
	)
}

func wrapPathError(operation, path string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s %s: %w", operation, path, err)
}
