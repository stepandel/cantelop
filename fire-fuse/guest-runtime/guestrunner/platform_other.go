//go:build !linux

package guestrunner

import "errors"

type systemPlatform struct{}

var errLinuxRequired = errors.New("guest workloads require Linux")

func (systemPlatform) lockOSThread()                 {}
func (systemPlatform) privateMountNamespace() error  { return errLinuxRequired }
func (systemPlatform) ensureNullDevice(string) error { return errLinuxRequired }
func (systemPlatform) mountMinimalDevices(string, string) error {
	return errLinuxRequired
}
func (systemPlatform) bindMount(string, string, bool) error    { return errLinuxRequired }
func (systemPlatform) protectRuntimeMount(string) error        { return errLinuxRequired }
func (systemPlatform) mountProc(string) error                  { return errLinuxRequired }
func (systemPlatform) mountSys(string) error                   { return errLinuxRequired }
func (systemPlatform) chroot(string) error                     { return errLinuxRequired }
func (systemPlatform) chdir(string) error                      { return errLinuxRequired }
func (systemPlatform) restrictPrivileges(uint32, uint32) error { return errLinuxRequired }
func (systemPlatform) exec([]string, []string) error           { return errLinuxRequired }
