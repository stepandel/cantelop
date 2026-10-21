//go:build linux

package guestassignment

import (
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestVSockConnectionDeadlinesUseSocketTimeouts(t *testing.T) {
	descriptors, err := unix.Socketpair(
		unix.AF_UNIX,
		unix.SOCK_STREAM|unix.SOCK_CLOEXEC,
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(descriptors[1])
	file := os.NewFile(uintptr(descriptors[0]), "deadline-test")
	if file == nil {
		t.Fatal("os.NewFile() = nil")
	}
	defer file.Close()
	connection := &vsockConnection{file: file}

	if err := connection.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("SetDeadline() error = %v", err)
	}
	for _, option := range []int{unix.SO_RCVTIMEO, unix.SO_SNDTIMEO} {
		value, err := unix.GetsockoptTimeval(descriptors[0], unix.SOL_SOCKET, option)
		if err != nil {
			t.Fatalf("GetsockoptTimeval(%d) error = %v", option, err)
		}
		if value.Sec == 0 && value.Usec == 0 {
			t.Errorf("socket timeout %d was not set", option)
		}
	}

	if err := connection.SetDeadline(time.Time{}); err != nil {
		t.Fatalf("clear SetDeadline() error = %v", err)
	}
}

func TestVSockHostCIDIsDistinctFromGuestAndWildcardCIDs(t *testing.T) {
	if unix.VMADDR_CID_HOST == unix.VMADDR_CID_ANY || unix.VMADDR_CID_HOST == unix.VMADDR_CID_LOCAL {
		t.Fatalf("host CID %d is not a distinct authority", unix.VMADDR_CID_HOST)
	}
}
