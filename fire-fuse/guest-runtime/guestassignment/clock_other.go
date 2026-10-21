//go:build !linux

package guestassignment

import "errors"

func setRealtimeClock(int64) error {
	return errors.New("realtime clock synchronization requires Linux")
}
