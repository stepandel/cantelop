package model

import (
	"errors"
	"testing"
	"time"
)

func TestComputeTenantValidation(t *testing.T) {
	if err := (ComputeTenant{ID: "usr_alice", CreatedAt: time.Now()}).Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
	if err := (ComputeTenant{ID: "alice", CreatedAt: time.Now()}).Validate(); !errors.Is(err, ErrInvalidComputeTenant) {
		t.Fatalf("Validate() = %v, want ErrInvalidComputeTenant", err)
	}
}
