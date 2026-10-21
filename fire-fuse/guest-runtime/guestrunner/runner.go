// Package guestrunner launches one container-image process inside the
// dedicated fire-fuse microVM.
package guestrunner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/stepandel/cantelop/fire-fuse/guest-runtime/guestconfig"
)

const (
	ConfigPath        = "/run/fire-fuse/workload.json"
	RootFSPath        = "/run/fire-fuse/container-rootfs"
	PlatformBunPath   = "/usr/local/lib/cantelop/bun"
	HostWorkspacePath = "/workspace"
	hostDevicePath    = "/dev"
	hostResolverPath  = "/etc/resolv.conf"
	containerDevice   = "/dev"
	containerProc     = "/proc"
	containerSys      = "/sys"
	containerResolver = "/etc/resolv.conf"
	// Platform SDK artifacts are complete deployment artifacts. Disable Bun's
	// fallback package installation so a workload cannot mutate its image or
	// reach a package registry while it is starting.
	platformBunNoInstallFlag = "--no-install"
)

var minimalContainerDevices = []string{
	"null",
	"zero",
	"full",
	"random",
	"urandom",
	"tty",
}

type deviceLink struct {
	name        string
	destination string
}

var minimalContainerDeviceLinks = []deviceLink{
	{name: "fd", destination: "/proc/self/fd"},
	{name: "stdin", destination: "/proc/self/fd/0"},
	{name: "stdout", destination: "/proc/self/fd/1"},
	{name: "stderr", destination: "/proc/self/fd/2"},
	{name: "ptmx", destination: "pts/ptmx"},
}

type platform interface {
	lockOSThread()
	privateMountNamespace() error
	ensureNullDevice(path string) error
	mountMinimalDevices(source, target string) error
	bindMount(source, target string, recursive bool) error
	protectRuntimeMount(target string) error
	mountProc(target string) error
	mountSys(target string) error
	chroot(path string) error
	chdir(path string) error
	restrictPrivileges(uid, gid uint32) error
	exec(args, env []string) error
}

type runtimePaths struct {
	rootFS      string
	workspace   string
	dev         string
	resolver    string
	platformBun string
}

var defaultRuntimePaths = runtimePaths{
	rootFS:      RootFSPath,
	workspace:   HostWorkspacePath,
	dev:         hostDevicePath,
	resolver:    hostResolverPath,
	platformBun: PlatformBunPath,
}

// Run applies config to the mounted container root filesystem and replaces the
// runner with the configured process.
func Run(config guestconfig.Config) error {
	return run(config, systemPlatform{}, defaultRuntimePaths)
}

func run(config guestconfig.Config, system platform, paths runtimePaths) error {
	if err := config.Validate(); err != nil {
		return fmt.Errorf("validate workload configuration: %w", err)
	}
	if err := requireDirectory(paths.rootFS); err != nil {
		return err
	}
	// Mount namespaces are thread-local on Linux. Keep every operation from
	// unshare through exec on this OS thread so the workload cannot enter a
	// different namespace if the Go scheduler migrates this goroutine.
	//
	// The thread is intentionally never unlocked: exec replaces the process on
	// success, and the dedicated guest launcher exits on any returned error.
	system.lockOSThread()
	if err := system.privateMountNamespace(); err != nil {
		return fmt.Errorf("create private mount namespace: %w", err)
	}
	if err := system.ensureNullDevice(filepath.Join(paths.dev, "null")); err != nil {
		return fmt.Errorf("prepare container null device: %w", err)
	}

	deviceTarget, err := ensureDirectory(paths.rootFS, containerDevice, 0o755)
	if err != nil {
		return err
	}
	if err := system.mountMinimalDevices(paths.dev, deviceTarget); err != nil {
		return fmt.Errorf("mount minimal %s: %w", containerDevice, err)
	}

	procTarget, err := ensureDirectory(paths.rootFS, containerProc, 0o755)
	if err != nil {
		return err
	}
	if err := system.mountProc(procTarget); err != nil {
		return fmt.Errorf("mount %s: %w", containerProc, err)
	}
	sysTarget, err := ensureDirectory(paths.rootFS, containerSys, 0o755)
	if err != nil {
		return err
	}
	if err := system.mountSys(sysTarget); err != nil {
		return fmt.Errorf("mount %s: %w", containerSys, err)
	}

	resolverTarget, err := ensureRegularFile(paths.rootFS, containerResolver)
	if err != nil {
		return err
	}
	if err := system.bindMount(paths.resolver, resolverTarget, false); err != nil {
		return fmt.Errorf("bind mount %s: %w", containerResolver, err)
	}

	if config.Process.Args[0] == guestconfig.PlatformRuntimeContainerPath {
		if err := requireRegularExecutable(paths.platformBun); err != nil {
			return err
		}
		runtimeTarget, err := ensureRegularFile(paths.rootFS, guestconfig.PlatformRuntimeContainerPath)
		if err != nil {
			return err
		}
		if err := system.bindMount(paths.platformBun, runtimeTarget, false); err != nil {
			return fmt.Errorf("bind platform Bun runtime: %w", err)
		}
		if err := system.protectRuntimeMount(runtimeTarget); err != nil {
			return fmt.Errorf("protect platform Bun runtime mount: %w", err)
		}
	}

	if config.Workspace.Enabled {
		if err := requireDirectory(paths.workspace); err != nil {
			return err
		}
		workspaceTarget, err := ensureDirectory(
			paths.rootFS,
			config.Workspace.ContainerPath,
			0o755,
		)
		if err != nil {
			return err
		}
		if err := system.bindMount(paths.workspace, workspaceTarget, false); err != nil {
			return fmt.Errorf("bind mount workspace: %w", err)
		}
	}

	if err := system.chroot(paths.rootFS); err != nil {
		return fmt.Errorf("enter container root filesystem: %w", err)
	}
	if err := system.chdir(config.Process.WorkingDirectory); err != nil {
		return fmt.Errorf("enter working directory %q: %w", config.Process.WorkingDirectory, err)
	}
	if err := system.restrictPrivileges(config.Process.UID, config.Process.GID); err != nil {
		return fmt.Errorf(
			"restrict process privileges for identity %d:%d: %w",
			config.Process.UID,
			config.Process.GID,
			err,
		)
	}
	args := config.Process.Args
	if args[0] == guestconfig.PlatformRuntimeContainerPath {
		args = []string{args[0], platformBunNoInstallFlag, args[1]}
		if _, managed := config.Process.SessionContract(); managed {
			args = append([]string{guestconfig.SessionLoader, "--library-path", guestconfig.SessionLibraryPath}, args...)
		}
	}
	if err := system.exec(args, config.Process.Env); err != nil {
		return fmt.Errorf("execute %q: %w", config.Process.Args[0], err)
	}
	return nil
}

func requireRegularExecutable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("inspect platform runtime %s: %w", path, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("platform runtime %s is not an executable regular file", path)
	}
	return nil
}

func requireDirectory(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("inspect directory %s: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}
	return nil
}

func ensureDirectory(root, containerPath string, mode os.FileMode) (string, error) {
	current := filepath.Clean(root)
	if err := requireDirectory(current); err != nil {
		return "", err
	}
	for _, component := range strings.Split(strings.TrimPrefix(containerPath, "/"), "/") {
		if component == "" {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		switch {
		case os.IsNotExist(err):
			if err := os.Mkdir(current, mode); err != nil {
				return "", fmt.Errorf("create container directory %s: %w", containerPath, err)
			}
		case err != nil:
			return "", fmt.Errorf("inspect container directory %s: %w", containerPath, err)
		case !info.IsDir():
			return "", fmt.Errorf("container directory %s crosses a non-directory", containerPath)
		}
	}
	return current, nil
}

func ensureRegularFile(root, containerPath string) (string, error) {
	parent, err := ensureDirectory(root, filepath.Dir(containerPath), 0o755)
	if err != nil {
		return "", err
	}
	target := filepath.Join(parent, filepath.Base(containerPath))
	info, err := os.Lstat(target)
	switch {
	case os.IsNotExist(err):
		file, createErr := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if createErr != nil {
			return "", fmt.Errorf("create container file %s: %w", containerPath, createErr)
		}
		if closeErr := file.Close(); closeErr != nil {
			return "", fmt.Errorf("close container file %s: %w", containerPath, closeErr)
		}
	case err != nil:
		return "", fmt.Errorf("inspect container file %s: %w", containerPath, err)
	case !info.Mode().IsRegular():
		return "", fmt.Errorf("container file %s is not a regular file", containerPath)
	}
	return target, nil
}
