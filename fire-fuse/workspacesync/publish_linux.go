package workspacesync

import "golang.org/x/sys/unix"

func renameNoReplace(source int, temporary string, target int, name string) error {
	return unix.Renameat2(source, temporary, target, name, unix.RENAME_NOREPLACE)
}
