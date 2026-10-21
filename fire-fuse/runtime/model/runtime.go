package model

// State describes the lifecycle phase of a Session Sandbox.
type State string

const (
	// StateActive and StateIdle are derived Session states. Sandbox lifecycle
	// records use the states below.
	StateActive     State = "active"
	StateIdle       State = "idle"
	StateStarting   State = "starting"
	StateRunning    State = "running"
	StateStopping   State = "stopping"
	StateTerminated State = "terminated"
	StateFailed     State = "failed"
)

// FailureReason is a stable, non-sensitive lifecycle failure category.
type FailureReason string

const (
	FailureRuntimeStart FailureReason = "runtime_start_failed"
	FailureRuntimeExit  FailureReason = "runtime_exited"
)

// NetworkAssignment is the guest-visible network identity of a Sandbox.
type NetworkAssignment struct {
	GuestIP   string `json:"guest_ip"`
	GatewayIP string `json:"gateway_ip"`
	Subnet    string `json:"subnet"`
	GuestMAC  string `json:"guest_mac"`
}

// CanAccessDatabase reports whether a Sandbox in this state may receive
// workspace database credentials.
func CanAccessDatabase(state State) bool {
	return state == StateStarting || state == StateRunning
}

// CanTransition reports whether moving directly between two states is valid.
func CanTransition(from, to State) bool {
	switch from {
	case StateStarting:
		return to == StateRunning || to == StateStopping || to == StateFailed
	case StateRunning:
		return to == StateStopping || to == StateFailed
	case StateStopping:
		return to == StateTerminated || to == StateFailed
	case StateFailed:
		return to == StateStopping || to == StateTerminated
	case StateTerminated:
		return false
	default:
		return false
	}
}
