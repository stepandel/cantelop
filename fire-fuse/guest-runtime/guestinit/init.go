package guestinit

import (
	"errors"
	"fmt"
	"os"
	"time"
)

const (
	platformDevice      = "/dev/vda"
	platformLower       = "/run/fire-fuse/platform-lower"
	platformOverlay     = "/run/fire-fuse/platform-overlay"
	platformUpper       = platformOverlay + "/upper"
	platformWork        = platformOverlay + "/work"
	platformMerged      = "/run/fire-fuse/platform-root"
	platformUpperSize   = "128m"
	platformDeviceWait  = 5 * time.Second
	platformInit        = "/sbin/init"
	platformInitPath    = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	errPlatformInitExec = "platform init returned without replacing the process"
)

var ErrLinuxRequired = errors.New("platform root initialization requires Linux")

type platform interface {
	mkdirAll(string, os.FileMode) error
	mountDev(string) error
	mountProc(string) error
	mountSys(string) error
	waitForBlockDevice(string, time.Duration) error
	mountLower(string, string) error
	mountUpper(string, string) error
	mountOverlay(string, string, string, string) error
	moveMount(string, string) error
	switchRoot(string) error
	exec(string, []string, []string) error
}

// Run constructs an ephemeral writable platform root over the read-only base
// drive and replaces the initramfs process with the platform's init.
func Run() error {
	return run(systemPlatform{})
}

func run(system platform) error {
	for _, directory := range []string{
		"/dev",
		"/proc",
		"/sys",
		platformLower,
		platformOverlay,
		platformMerged,
	} {
		if err := system.mkdirAll(directory, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", directory, err)
		}
	}

	if err := system.mountDev("/dev"); err != nil {
		return fmt.Errorf("mount devtmpfs: %w", err)
	}
	if err := system.mountProc("/proc"); err != nil {
		return fmt.Errorf("mount proc: %w", err)
	}
	if err := system.mountSys("/sys"); err != nil {
		return fmt.Errorf("mount sysfs: %w", err)
	}
	if err := system.waitForBlockDevice(platformDevice, platformDeviceWait); err != nil {
		return fmt.Errorf("wait for platform root device: %w", err)
	}
	if err := system.mountLower(platformDevice, platformLower); err != nil {
		return fmt.Errorf("mount platform lower: %w", err)
	}
	if err := system.mountUpper(platformOverlay, platformUpperSize); err != nil {
		return fmt.Errorf("mount platform upper: %w", err)
	}
	for _, directory := range []string{platformUpper, platformWork} {
		if err := system.mkdirAll(directory, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", directory, err)
		}
	}
	if err := system.mountOverlay(
		platformLower,
		platformUpper,
		platformWork,
		platformMerged,
	); err != nil {
		return fmt.Errorf("mount platform overlay: %w", err)
	}
	for _, directory := range []string{
		platformMerged + "/dev",
		platformMerged + "/proc",
		platformMerged + "/sys",
	} {
		if err := system.mkdirAll(directory, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", directory, err)
		}
	}

	for _, mount := range []struct {
		source string
		target string
	}{
		{source: "/dev", target: platformMerged + "/dev"},
		{source: "/proc", target: platformMerged + "/proc"},
		{source: "/sys", target: platformMerged + "/sys"},
	} {
		if err := system.moveMount(mount.source, mount.target); err != nil {
			return fmt.Errorf("move %s into platform root: %w", mount.source, err)
		}
	}
	if err := system.switchRoot(platformMerged); err != nil {
		return fmt.Errorf("switch platform root: %w", err)
	}
	if err := system.exec(
		platformInit,
		[]string{platformInit},
		[]string{
			"HOME=/",
			"TERM=linux",
			"PATH=" + platformInitPath,
		},
	); err != nil {
		return fmt.Errorf("execute platform init: %w", err)
	}
	return errors.New(errPlatformInitExec)
}
