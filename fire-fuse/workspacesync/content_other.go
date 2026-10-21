//go:build !linux

package workspacesync

import (
	"os"
	"syscall"
)

// Non-Linux use is the user's unprivileged local CLI adapter. Hosted storage is
// Linux and uses O_PATH before opening any untrusted inode for content access.
func openContent(root *os.Root, name string) (*os.File, error) {
	return root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_NOCTTY, 0)
}
