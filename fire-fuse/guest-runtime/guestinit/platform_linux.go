//go:build linux

package guestinit

import (
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

type systemPlatform struct{}

func (systemPlatform) mkdirAll(path string, mode os.FileMode) error {
	return os.MkdirAll(path, mode)
}

func (systemPlatform) mountDev(target string) error {
	return unix.Mount(
		"devtmpfs",
		target,
		"devtmpfs",
		unix.MS_NOSUID,
		"mode=0755",
	)
}

func (systemPlatform) mountProc(target string) error {
	return unix.Mount(
		"proc",
		target,
		"proc",
		unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC,
		"",
	)
}

func (systemPlatform) mountSys(target string) error {
	return unix.Mount(
		"sysfs",
		target,
		"sysfs",
		unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC,
		"",
	)
}

func (systemPlatform) waitForBlockDevice(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		info, err := os.Stat(path)
		switch {
		case err == nil && info.Mode()&os.ModeDevice != 0 &&
			info.Mode()&os.ModeCharDevice == 0:
			return nil
		case err == nil:
			return fmt.Errorf("%s is not a block device", path)
		case !errors.Is(err, os.ErrNotExist):
			return err
		case !time.Now().Before(deadline):
			return fmt.Errorf("timed out after %s", timeout)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (systemPlatform) mountLower(device, target string) error {
	return unix.Mount(
		device,
		target,
		"ext4",
		unix.MS_RDONLY|unix.MS_NODEV|unix.MS_NOSUID,
		"",
	)
}

func (systemPlatform) mountUpper(target, size string) error {
	return unix.Mount(
		"tmpfs",
		target,
		"tmpfs",
		unix.MS_NODEV|unix.MS_NOSUID,
		"mode=0755,size="+size,
	)
}

func (systemPlatform) mountOverlay(lower, upper, work, target string) error {
	return unix.Mount(
		"overlay",
		target,
		"overlay",
		unix.MS_NODEV|unix.MS_NOSUID,
		"lowerdir="+lower+",upperdir="+upper+",workdir="+work,
	)
}

func (systemPlatform) moveMount(source, target string) error {
	return unix.Mount(source, target, "", unix.MS_MOVE, "")
}

func (systemPlatform) switchRoot(path string) error {
	if err := unix.Chdir(path); err != nil {
		return err
	}
	if err := unix.Mount(".", "/", "", unix.MS_MOVE, ""); err != nil {
		return err
	}
	if err := unix.Chroot("."); err != nil {
		return err
	}
	return unix.Chdir("/")
}

func (systemPlatform) exec(path string, args, env []string) error {
	return unix.Exec(path, args, env)
}
