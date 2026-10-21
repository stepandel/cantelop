package microvm

import "testing"

func TestCanTransition(t *testing.T) {
	tests := []struct {
		name string
		from State
		to   State
		want bool
	}{
		{name: "started", from: StateStarting, to: StateRunning, want: true},
		{name: "terminate while starting", from: StateStarting, to: StateStopping, want: true},
		{name: "runtime failure", from: StateRunning, to: StateFailed, want: true},
		{name: "cleanup failure", from: StateFailed, to: StateStopping, want: true},
		{name: "stopped", from: StateStopping, to: StateTerminated, want: true},
		{name: "cannot restart", from: StateTerminated, to: StateStarting, want: false},
		{name: "cannot skip cleanup", from: StateRunning, to: StateTerminated, want: false},
		{name: "unknown source", from: State("unknown"), to: StateFailed, want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := CanTransition(test.from, test.to); got != test.want {
				t.Fatalf("CanTransition(%q, %q) = %t, want %t", test.from, test.to, got, test.want)
			}
		})
	}
}
