//go:build linux

package guestrunner

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/sys/unix"
)

type systemPlatform struct{}

func (systemPlatform) lockOSThread() {
	runtime.LockOSThread()
}

func (systemPlatform) privateMountNamespace() error {
	if err := unix.Unshare(unix.CLONE_NEWNS); err != nil {
		return err
	}
	return unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, "")
}

func (systemPlatform) ensureNullDevice(path string) error {
	info, err := os.Stat(path)
	switch {
	case err == nil:
		if info.Mode()&os.ModeDevice == 0 ||
			info.Mode()&os.ModeCharDevice == 0 {
			return fmt.Errorf("%s is not a character device", path)
		}
		return nil
	case !errors.Is(err, os.ErrNotExist):
		return err
	}
	if err := unix.Mknod(path, unix.S_IFCHR|0o666, int(unix.Mkdev(1, 3))); err != nil {
		return err
	}
	return unix.Chmod(path, 0o666)
}

func (systemPlatform) mountMinimalDevices(sourceRoot, target string) error {
	if err := unix.Mount(
		"tmpfs",
		target,
		"tmpfs",
		unix.MS_NOSUID|unix.MS_NOEXEC,
		"mode=0755,size=1m",
	); err != nil {
		return err
	}
	for _, directory := range []struct {
		name string
		mode os.FileMode
	}{
		{name: "pts", mode: 0o755},
		{name: "shm", mode: 0o1777},
	} {
		if err := os.Mkdir(filepath.Join(target, directory.name), directory.mode); err != nil {
			return err
		}
	}
	if err := unix.Mount(
		"devpts",
		filepath.Join(target, "pts"),
		"devpts",
		unix.MS_NOSUID|unix.MS_NOEXEC,
		"newinstance,ptmxmode=0666,mode=0620,gid=5",
	); err != nil {
		return err
	}
	if err := unix.Mount(
		"tmpfs",
		filepath.Join(target, "shm"),
		"tmpfs",
		unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC,
		"mode=1777,size=64m",
	); err != nil {
		return err
	}
	for _, name := range minimalContainerDevices {
		source := filepath.Join(sourceRoot, name)
		info, err := os.Stat(source)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeDevice == 0 || info.Mode()&os.ModeCharDevice == 0 {
			return fmt.Errorf("%s is not a character device", source)
		}
		destination := filepath.Join(target, name)
		file, err := os.OpenFile(
			destination,
			os.O_WRONLY|os.O_CREATE|os.O_EXCL,
			0o600,
		)
		if err != nil {
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		if err := unix.Mount(source, destination, "", unix.MS_BIND, ""); err != nil {
			return err
		}
		if err := unix.MountSetattr(
			unix.AT_FDCWD,
			destination,
			0,
			&unix.MountAttr{
				Attr_set: unix.MOUNT_ATTR_NOSUID | unix.MOUNT_ATTR_NOEXEC,
			},
		); err != nil {
			return err
		}
	}
	for _, link := range minimalContainerDeviceLinks {
		if err := os.Symlink(link.destination, filepath.Join(target, link.name)); err != nil {
			return err
		}
	}
	return nil
}

func (systemPlatform) bindMount(source, target string, recursive bool) error {
	flags := uintptr(unix.MS_BIND)
	if recursive {
		flags |= unix.MS_REC
	}
	return unix.Mount(source, target, "", flags, "")
}

func (systemPlatform) protectRuntimeMount(target string) error {
	return unix.MountSetattr(
		unix.AT_FDCWD,
		target,
		0,
		&unix.MountAttr{Attr_set: unix.MOUNT_ATTR_RDONLY | unix.MOUNT_ATTR_NOSUID | unix.MOUNT_ATTR_NODEV},
	)
}

func (systemPlatform) mountProc(target string) error {
	return unix.Mount(
		"proc",
		target,
		"proc",
		unix.MS_RDONLY|unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC,
		"hidepid=2",
	)
}

func (systemPlatform) mountSys(target string) error {
	return unix.Mount(
		"sysfs",
		target,
		"sysfs",
		unix.MS_RDONLY|unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC,
		"",
	)
}

func (systemPlatform) chroot(path string) error {
	return unix.Chroot(path)
}

func (systemPlatform) chdir(path string) error {
	return unix.Chdir(path)
}

func (systemPlatform) restrictPrivileges(uid, gid uint32) error {
	return restrictWorkloadPrivileges(uid, gid)
}

func (systemPlatform) exec(args, env []string) error {
	executable, err := lookPath(args[0], env)
	if err != nil {
		return err
	}
	return unix.Exec(executable, args, env)
}

func lookPath(name string, env []string) (string, error) {
	if strings.ContainsRune(name, '/') {
		return name, nil
	}
	searchPath := "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	for _, variable := range env {
		if value, found := strings.CutPrefix(variable, "PATH="); found {
			searchPath = value
			break
		}
	}
	for _, directory := range filepath.SplitList(searchPath) {
		if directory == "" {
			directory = "."
		}
		candidate := filepath.Join(directory, name)
		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() {
			continue
		}
		if err := unix.Access(candidate, unix.X_OK); err == nil {
			return candidate, nil
		}
	}
	return "", errors.New("executable file not found in PATH")
}
