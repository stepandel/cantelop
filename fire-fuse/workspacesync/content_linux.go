package workspacesync

import (
	"errors"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// O_PATH inspects an inode without opening a customer-supplied device or FIFO.
// Reopen only a verified regular inode through its still-pinned descriptor.
func openContent(root *os.Root, name string) (*os.File, error) {
	file, err := root.OpenFile(name, unix.O_PATH|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("unsupported file")
	}
	return os.Open("/proc/self/fd/" + strconv.FormatUint(uint64(file.Fd()), 10))
}
