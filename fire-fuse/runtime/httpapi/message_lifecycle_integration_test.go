package httpapi_test

import "time"

const (
	lifecycleMessageID = "msg_0123456789abcdef0123456789abcdef"
	lifecycleRequestID = "req_fedcba9876543210fedcba9876543210"
	lifecycleWorkspace = "wsp_0123456789abcdef0123456789abcdef"
)

func waitUntil(timeout time.Duration, condition func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}
