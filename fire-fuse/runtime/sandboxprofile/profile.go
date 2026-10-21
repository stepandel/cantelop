// Package sandboxprofile defines the platform-owned sandbox shapes available
// to Apps and host configuration.
package sandboxprofile

import (
	"errors"
	"fmt"
)

const (
	DefaultName      = "default"
	DefaultVCPUs     = 4
	DefaultMemoryMiB = 512
	HotplugMemoryMiB = 4096
)

var ErrUnsupported = errors.New("unsupported sandbox profile")

type Profile struct {
	Name      string
	VCPUs     int
	MemoryMiB int
}

func Resolve(name string) (Profile, error) {
	if name != DefaultName {
		return Profile{}, fmt.Errorf("%w: %q", ErrUnsupported, name)
	}
	return Profile{Name: DefaultName, VCPUs: DefaultVCPUs, MemoryMiB: DefaultMemoryMiB}, nil
}
