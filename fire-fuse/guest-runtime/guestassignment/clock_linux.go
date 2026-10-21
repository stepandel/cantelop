//go:build linux

package guestassignment

import "golang.org/x/sys/unix"

func setRealtimeClock(unixNano int64) error {
	timestamp := unix.NsecToTimespec(unixNano)
	return unix.ClockSettime(unix.CLOCK_REALTIME, &timestamp)
}
