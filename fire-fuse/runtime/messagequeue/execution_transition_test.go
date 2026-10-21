package messagequeue

import (
	"errors"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/messageprotocol"
)

type fakeEvidence struct {
	bound, live bool
	queries     int
}

func (f *fakeEvidence) SessionOnRunningSandbox(string, string, string) (bool, error) {
	f.queries++
	return f.bound, nil
}

func (f *fakeEvidence) SandboxLive(string) (bool, error) {
	f.queries++
	return f.live, nil
}

func transitionFixture() (Execution, Message, time.Time) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	e := Execution{
		MessageID: "msg_0123456789abcdef0123456789abcdef", SandboxID: "sbx-0123456789abcdef0123456789abcdef",
		SessionID: "ses_0123456789abcdef0123456789abcdef", Attempt: 1, Phase: messageprotocol.PhaseSending,
		AdmissionDeadline: now.Add(30 * time.Second), ExecutionDeadline: now.Add(5 * time.Minute),
		RedeliverySupported: true,
	}
	message := Message{ID: e.MessageID, WorkspaceID: "wsp_0123456789abcdef0123456789abcdef", Status: StatusHandling, Attempts: 1}
	return e, message, now
}

func TestFirstExecutionSaveRequiresRunningBinding(t *testing.T) {
	e, message, now := transitionFixture()
	if _, _, err := e.Transition(nil, message, &fakeEvidence{}, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("unbound Session = %v", err)
	}
	next, unchanged, err := e.Transition(nil, message, &fakeEvidence{bound: true}, now)
	if err != nil || unchanged || !next.PhaseAt.Equal(now) {
		t.Fatalf("bound Session = %#v %v %v", next, unchanged, err)
	}
}

func TestExecutionPhaseNeverMovesBackwards(t *testing.T) {
	e, message, now := transitionFixture()
	previous := e
	previous.Phase = messageprotocol.PhaseRunning
	previous.Receipt = &messageprotocol.Receipt{State: messageprotocol.ReceiptRunning}
	e.Phase = messageprotocol.PhaseAccepted
	e.Receipt = previous.Receipt
	evidence := &fakeEvidence{}
	if _, _, err := e.Transition(&previous, message, evidence, now); !errors.Is(err, ErrConflict) || evidence.queries != 0 {
		t.Fatalf("backwards phase = %v after %d queries", err, evidence.queries)
	}
	if _, unchanged, err := previous.Transition(&previous, message, evidence, now); err != nil || !unchanged {
		t.Fatalf("replay = %v %v", unchanged, err)
	}
}

func TestRetryingRequiresStoppedSandboxAndAttemptBudget(t *testing.T) {
	e, message, now := transitionFixture()
	previous := e
	e.Phase = messageprotocol.PhaseRetrying
	if _, _, err := e.Transition(&previous, message, &fakeEvidence{live: true}, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("live Sandbox = %v", err)
	}
	if _, _, err := e.Transition(&previous, message, &fakeEvidence{}, now); err != nil {
		t.Fatalf("stopped Sandbox = %v", err)
	}
	e.Attempt, previous.Attempt, message.Attempts = MaxAttempts, MaxAttempts, MaxAttempts
	if _, _, err := e.Transition(&previous, message, &fakeEvidence{}, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("exhausted attempts = %v", err)
	}
}

func TestSettlementMapsTerminalReceipts(t *testing.T) {
	for _, tc := range []struct {
		state  messageprotocol.ReceiptState
		code   string
		status Status
		want   string
	}{
		{messageprotocol.ReceiptSucceeded, "", StatusHandled, ""},
		{messageprotocol.ReceiptFailed, "", StatusFailed, "message_handler_failed"},
		{messageprotocol.ReceiptFailed, "app_rejected", StatusFailed, "app_rejected"},
		{messageprotocol.ReceiptTimedOut, "", StatusFailed, "message_timed_out"},
		{messageprotocol.ReceiptTimedOut, "app_deadline", StatusFailed, "app_deadline"},
	} {
		e := Execution{Receipt: &messageprotocol.Receipt{State: tc.state, ErrorCode: tc.code}}
		if status, code := e.Settlement(); status != tc.status || code != tc.want {
			t.Fatalf("%s/%s = %s %s", tc.state, tc.code, status, code)
		}
	}
}
