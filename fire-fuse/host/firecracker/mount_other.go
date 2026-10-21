//go:build !linux

package firecracker

import "errors"

var errReadOnlyBindRequiresLinux = errors.New("read-only bind mounts require Linux")

func bindReadOnlyFile(string, string) error {
	return errReadOnlyBindRequiresLinux
}

func unmountReadOnlyFile(string) error {
	return nil
}
