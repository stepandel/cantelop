package model

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSessionValidation(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	valid := Session{
		ID:               "ses_worker-1",
		WorkspaceID:      "wsp_primary",
		BuildID:          "sha256:" + strings.Repeat("a", 64),
		KeepAliveSeconds: 5 * 60,
		State:            StateActive,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
	zeroKeepAlive := valid
	zeroKeepAlive.KeepAliveSeconds = 0
	if err := zeroKeepAlive.Validate(); err != nil {
		t.Fatalf("Validate() with zero keep-alive = %v", err)
	}

	for _, state := range []State{StateStarting, StateStopping, StateFailed, StateTerminated, State("paused")} {
		invalid := valid
		invalid.State = state
		if err := invalid.Validate(); !errors.Is(err, ErrInvalidSession) {
			t.Fatalf("Validate() with state %q = %v, want ErrInvalidSession", state, err)
		}
	}
}

func TestCanonicalRuntimeIdentifiers(t *testing.T) {
	if err := ValidateSessionID("ses_worker-1"); err != nil {
		t.Fatalf("ValidateSessionID() = %v", err)
	}
	if err := ValidateSessionID("sandbox-1"); err == nil {
		t.Fatal("ValidateSessionID() accepted a Sandbox ID")
	}
	if err := ValidateBuildID("sha256:" + strings.Repeat("f", 64)); err != nil {
		t.Fatalf("ValidateBuildID() = %v", err)
	}
	if err := ValidateBuildID("latest"); err == nil {
		t.Fatal("ValidateBuildID() accepted a mutable tag")
	}
}
