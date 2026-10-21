package microvm

import "github.com/stepandel/cantelop/fire-fuse/runtime/sandboxprofile"

const (
	DefaultSandboxProfileName = sandboxprofile.DefaultName
	DefaultSandboxVCPUs       = sandboxprofile.DefaultVCPUs
	DefaultSandboxMemoryMiB   = sandboxprofile.DefaultMemoryMiB
	SandboxHotplugMemoryMiB   = sandboxprofile.HotplugMemoryMiB
)

var ErrUnsupportedSandboxProfile = sandboxprofile.ErrUnsupported

// SandboxProfile is a platform-owned isolation shape. Apps select a profile
// by name; they never own or override its resource limits.
type SandboxProfile = sandboxprofile.Profile

// ResolveSandboxProfile returns the one isolation profile supported by the
// platform. Keeping this closed prevents App configuration from becoming an
// indirect resource-sizing API.
func ResolveSandboxProfile(name string) (SandboxProfile, error) {
	return sandboxprofile.Resolve(name)
}
