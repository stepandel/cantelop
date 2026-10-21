package model

import (
	"errors"
	"testing"
	"time"
)

func TestRequestEventValidation(t *testing.T) {
	valid := RequestEvent{
		EventID:     "request-1",
		WorkspaceID: "wsp_primary",
		SessionID:   "ses_worker-1",
		ReceivedAt:  time.Now(),
		StatusCode:  200,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}

	invalid := valid
	invalid.SessionID = "sandbox-1"
	if err := invalid.Validate(); !errors.Is(err, ErrInvalidRequestEvent) {
		t.Fatalf("Validate() = %v, want ErrInvalidRequestEvent", err)
	}
}

func TestRequestEventQueryValidation(t *testing.T) {
	if err := (RequestEventQuery{WorkspaceID: "wsp_primary", Limit: 100}).Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
	if err := (RequestEventQuery{AppID: "app-1", Limit: MaxRequestEventPageSize + 1}).Validate(); !errors.Is(err, ErrInvalidRequestEvent) {
		t.Fatalf("Validate() = %v, want ErrInvalidRequestEvent", err)
	}
	if err := (RequestEventQuery{WorkspaceID: "wsp_primary", AppID: "app-1", Limit: 1}).Validate(); !errors.Is(err, ErrInvalidRequestEvent) {
		t.Fatalf("Validate() = %v, want ErrInvalidRequestEvent", err)
	}
}
