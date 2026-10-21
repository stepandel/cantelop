package messagequeue

import (
	"encoding/json"
	"testing"
	"time"
)

func TestAcceptedMessageValidation(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	message := Message{
		ID:               "msg_0123456789abcdef0123456789abcdef",
		WorkspaceID:      "wsp_0123456789abcdef0123456789abcdef",
		LogicalSessionID: "agent:primary", KeepAliveSeconds: 300,
		Payload: json.RawMessage(`{"type":"prompt"}`), Status: StatusAccepted,
		WorkState: WorkPending, AcceptedAt: now, UpdatedAt: now,
	}
	if err := message.Validate(); err != nil {
		t.Fatal(err)
	}
	message.SessionID = "ses_too_early"
	if err := message.Validate(); err == nil {
		t.Fatal("Validate() accepted runtime state before delivery")
	}
}
