package workspacesync

import (
	"os"
	"path"
	"strings"
	"syscall"
)

// publishNew atomically moves a prepared file without replacing another writer's
// destination. A hard-link/unlink pair is unsuitable on trash-retaining mounts:
// unlink can retain a second link and make the published file unsafe to sync.
func publishNew(root *os.Root, temporary, destination string) error {
	parent, err := openDirectory(root, ".")
	if err != nil {
		return err
	}
	directory := path.Dir(destination)
	if directory != "." {
		for _, part := range strings.Split(directory, "/") {
			next, e := openDirectory(parent, part)
			parent.Close()
			if e != nil {
				return e
			}
			parent = next
		}
	}
	defer parent.Close()
	source, err := root.OpenFile(".", os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer source.Close()
	target, err := parent.OpenFile(".", os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer target.Close()
	return renameNoReplace(int(source.Fd()), temporary, int(target.Fd()), path.Base(destination))
}
