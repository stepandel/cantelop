//go:build !linux

package guestinit

import (
	"os"
	"time"
)

type systemPlatform struct{}

func (systemPlatform) mkdirAll(string, os.FileMode) error             { return ErrLinuxRequired }
func (systemPlatform) mountDev(string) error                          { return ErrLinuxRequired }
func (systemPlatform) mountProc(string) error                         { return ErrLinuxRequired }
func (systemPlatform) mountSys(string) error                          { return ErrLinuxRequired }
func (systemPlatform) waitForBlockDevice(string, time.Duration) error { return ErrLinuxRequired }
func (systemPlatform) mountLower(string, string) error                { return ErrLinuxRequired }
func (systemPlatform) mountUpper(string, string) error                { return ErrLinuxRequired }
func (systemPlatform) mountOverlay(string, string, string, string) error {
	return ErrLinuxRequired
}
func (systemPlatform) moveMount(string, string) error        { return ErrLinuxRequired }
func (systemPlatform) switchRoot(string) error               { return ErrLinuxRequired }
func (systemPlatform) exec(string, []string, []string) error { return ErrLinuxRequired }
