package sessionbroker

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestNewIDBoundsWorkspacePrefix(t *testing.T) {
	id, err := newID(strings.Repeat("a", 63), bytes.NewReader([]byte("12345678")))
	if err != nil {
		t.Fatal(err)
	}
	if len(id) != 64 || id != "ses_"+strings.Repeat("a", 43)+"_3132333435363738" {
		t.Fatalf("newID() = %q", id)
	}
}

func TestNewIDReportsEntropyFailure(t *testing.T) {
	if _, err := newID("workspace", failingReader{}); !errors.Is(err, ErrIdentity) {
		t.Fatalf("newID() error = %v, want ErrIdentity", err)
	}
}

func TestSessionIdentityScopes(t *testing.T) {
	if KeyHash("wsp_a", "thread") == KeyHash("wsp_b", "thread") {
		t.Fatal("KeyHash() did not scope the key to its Workspace")
	}
	if LogicalID("app_a", "thread") == LogicalID("app_b", "thread") {
		t.Fatal("LogicalID() did not scope the identity to its App")
	}
	if LogicalKeyHash("wsp_a", "thread") == KeyHash("wsp_a", "thread") {
		t.Fatal("LogicalKeyHash() collided with the legacy key namespace")
	}
	if got := LogicalID("app_a", "thread"); len(got) != 36 || !strings.HasPrefix(got, "ses_") {
		t.Fatalf("LogicalID() = %q", got)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("entropy unavailable") }
