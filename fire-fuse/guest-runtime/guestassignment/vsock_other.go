//go:build !linux

package guestassignment

import (
	"errors"
	"net"
)

var errVSockRequiresLinux = errors.New("guest vsock requires Linux")

// ListenVSock reports that Firecracker guest vsock is Linux-only.
func ListenVSock(uint32) (net.Listener, error) {
	return nil, errVSockRequiresLinux
}
