//go:build linux

package firecracker

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func bindReadOnlyFile(sourcePath, destinationPath string) (err error) {
	destination, err := os.OpenFile(
		destinationPath,
		os.O_WRONLY|os.O_CREATE|os.O_EXCL,
		0o600,
	)
	if err != nil {
		return err
	}
	if err := destination.Close(); err != nil {
		_ = os.Remove(destinationPath)
		return err
	}

	mounted := false
	defer func() {
		if err == nil {
			return
		}
		if mounted {
			_ = unix.Unmount(destinationPath, 0)
		}
		_ = os.Remove(destinationPath)
	}()

	if err = unix.Mount(sourcePath, destinationPath, "", unix.MS_BIND, ""); err != nil {
		return err
	}
	mounted = true
	err = unix.Mount(
		"",
		destinationPath,
		"",
		unix.MS_BIND|unix.MS_REMOUNT|unix.MS_RDONLY|unix.MS_NODEV|unix.MS_NOSUID,
		"",
	)
	return err
}

func unmountReadOnlyFile(path string) error {
	err := unix.Unmount(path, 0)
	if errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOENT) {
		return nil
	}
	return err
}
