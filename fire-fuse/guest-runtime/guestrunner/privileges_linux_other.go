//go:build linux && !amd64

package guestrunner

import (
	"fmt"
	"runtime"
)

func restrictWorkloadPrivileges(uint32, uint32) error {
	return fmt.Errorf("guest workload seccomp policy does not support Linux/%s", runtime.GOARCH)
}
