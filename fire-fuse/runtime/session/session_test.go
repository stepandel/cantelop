package session

import "testing"

func TestStateValid(t *testing.T) {
	for _, state := range []State{
		StateActive,
		StateIdle,
	} {
		if !state.Valid() {
			t.Errorf("state %q is not valid", state)
		}
	}
	if State("paused").Valid() {
		t.Fatal("unknown state is valid")
	}
}

func TestEndpointTargetURL(t *testing.T) {
	target, err := (Endpoint{Scheme: "https", Authority: "runtime.example:8443"}).TargetURL()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := target.String(), "https://runtime.example:8443"; got != want {
		t.Fatalf("target = %q, want %q", got, want)
	}
}

func TestEndpointTargetURLRejectsInvalidOrigins(t *testing.T) {
	for _, endpoint := range []Endpoint{
		{},
		{Scheme: "ftp", Authority: "runtime.example:21"},
		{Scheme: "http", Authority: " runtime.example:80"},
		{Scheme: "http", Authority: "runtime.example/path"},
		{Scheme: "http", Authority: "user@runtime.example"},
	} {
		if target, err := endpoint.TargetURL(); err == nil {
			t.Errorf("endpoint %+v returned target %v", endpoint, target)
		}
	}
}
