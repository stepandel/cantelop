package workspacesync

import "golang.org/x/sys/unix"

func renameNoReplace(source int, temporary string, target int, name string) error {
	return unix.RenameatxNp(source, temporary, target, name, unix.RENAME_EXCL)
}
