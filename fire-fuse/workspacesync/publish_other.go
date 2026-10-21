//go:build !linux && !darwin

package workspacesync

import "errors"

func renameNoReplace(source int, temporary string, target int, name string) error {
	return errors.ErrUnsupported
}
