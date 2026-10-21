//go:build linux

package guestassignment

import (
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// ListenVSock creates the guest-side Firecracker vsock listener.
func ListenVSock(port uint32) (net.Listener, error) {
	fileDescriptor, err := unix.Socket(
		unix.AF_VSOCK,
		unix.SOCK_STREAM|unix.SOCK_CLOEXEC,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("create vsock: %w", err)
	}
	listener := &vsockListener{
		fileDescriptor: fileDescriptor,
		address:        vsockAddress{cid: unix.VMADDR_CID_ANY, port: port},
	}
	if err := unix.Bind(fileDescriptor, &unix.SockaddrVM{
		CID:  unix.VMADDR_CID_ANY,
		Port: port,
	}); err != nil {
		listener.Close()
		return nil, fmt.Errorf("bind vsock port %d: %w", port, err)
	}
	if err := unix.Listen(fileDescriptor, 1); err != nil {
		listener.Close()
		return nil, fmt.Errorf("listen on vsock port %d: %w", port, err)
	}
	return listener, nil
}

type vsockListener struct {
	fileDescriptor int
	address        vsockAddress
	closeOnce      sync.Once
}

func (l *vsockListener) Accept() (net.Conn, error) {
	for {
		fileDescriptor, address, err := unix.Accept4(l.fileDescriptor, unix.SOCK_CLOEXEC)
		if err != nil {
			return nil, err
		}
		remote := vsockAddress{}
		if vmAddress, ok := address.(*unix.SockaddrVM); ok {
			remote.cid = vmAddress.CID
			remote.port = vmAddress.Port
		}
		if remote.cid != unix.VMADDR_CID_HOST {
			_ = unix.Close(fileDescriptor)
			continue
		}
		file := os.NewFile(uintptr(fileDescriptor), "fire-fuse-assignment-vsock")
		if file == nil {
			_ = unix.Close(fileDescriptor)
			return nil, fmt.Errorf("create vsock connection file")
		}
		return &vsockConnection{
			file:   file,
			local:  l.address,
			remote: remote,
		}, nil
	}
}

func (l *vsockListener) Close() error {
	var err error
	l.closeOnce.Do(func() {
		err = unix.Close(l.fileDescriptor)
	})
	return err
}

func (l *vsockListener) Addr() net.Addr {
	return l.address
}

type vsockConnection struct {
	file   *os.File
	local  vsockAddress
	remote vsockAddress
}

func (c *vsockConnection) Read(buffer []byte) (int, error) {
	return c.file.Read(buffer)
}

func (c *vsockConnection) Write(buffer []byte) (int, error) {
	return c.file.Write(buffer)
}

func (c *vsockConnection) Close() error {
	return c.file.Close()
}

func (c *vsockConnection) LocalAddr() net.Addr {
	return c.local
}

func (c *vsockConnection) RemoteAddr() net.Addr {
	return c.remote
}

func (c *vsockConnection) SetDeadline(deadline time.Time) error {
	if err := c.SetReadDeadline(deadline); err != nil {
		return err
	}
	return c.SetWriteDeadline(deadline)
}

func (c *vsockConnection) SetReadDeadline(deadline time.Time) error {
	return setSocketDeadline(c.file, unix.SO_RCVTIMEO, deadline)
}

func (c *vsockConnection) SetWriteDeadline(deadline time.Time) error {
	return setSocketDeadline(c.file, unix.SO_SNDTIMEO, deadline)
}

func setSocketDeadline(file *os.File, option int, deadline time.Time) error {
	timeout := time.Duration(0)
	if !deadline.IsZero() {
		timeout = time.Until(deadline)
		if timeout <= 0 {
			timeout = time.Microsecond
		}
	}
	value := unix.NsecToTimeval(timeout.Nanoseconds())
	if err := unix.SetsockoptTimeval(
		int(file.Fd()),
		unix.SOL_SOCKET,
		option,
		&value,
	); err != nil {
		return fmt.Errorf("set vsock deadline: %w", err)
	}
	return nil
}

type vsockAddress struct {
	cid  uint32
	port uint32
}

func (a vsockAddress) Network() string {
	return "vsock"
}

func (a vsockAddress) String() string {
	return fmt.Sprintf("%d:%d", a.cid, a.port)
}
