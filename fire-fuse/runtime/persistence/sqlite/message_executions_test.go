package sqlite

import (
	"github.com/stepandel/cantelop/fire-fuse/messageprotocol"
	"github.com/stepandel/cantelop/fire-fuse/runtime/messagequeue"
	"github.com/stepandel/cantelop/fire-fuse/runtime/model"
	"reflect"
	"testing"
	"time"
)

func TestExecutionReceiptRecoveryAndSandboxFence(t *testing.T) {
	r, db, at := newMessageDeliveryRepository(t)
	defer db.Close()
	const sandbox = "sbx-11111111111111111111111111111111"
	if _, err := db.Exec("INSERT INTO sandboxes VALUES (?, 'running', X'01')", sandbox); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE sessions SET sandbox_id=? WHERE id='ses_actor_a'", sandbox); err != nil {
		t.Fatal(err)
	}
	message := queuedMessage("msg_00000000000000000000000000000001", "agent:a", at)
	if _, _, err := r.Enqueue(message); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.ClaimNext(at); err != nil {
		t.Fatal(err)
	}
	e := messagequeue.Execution{MessageID: message.ID, Attempt: 1, SessionID: "ses_actor_a", SandboxID: sandbox, Phase: "sending", AdmissionDeadline: at.Add(30 * time.Second), ExecutionDeadline: at.Add(5 * time.Minute)}
	e, err := r.SaveExecution(e, at)
	if err != nil {
		t.Fatal(err)
	}
	if count, err := r.RequeueHandling(at); err != nil || count != 0 {
		t.Fatalf("recovery requeued bound message: %d %v", count, err)
	}
	stale := e
	e.Inspection = &model.RuntimeInspection{Category: "timeout", Error: "inspection deadline exceeded", FirstFailureAt: &at, LastFailureAt: &at, ConsecutiveFailures: 2}
	e.Phase = "accepted"
	e.Receipt = &messageprotocol.Receipt{SandboxID: sandbox, MessageID: message.ID, Sequence: 1, Generation: 1, State: "queued", AcceptedAt: at, Deadline: e.ExecutionDeadline}
	e, err = r.SaveExecution(e, at)
	if err != nil {
		t.Fatal(err)
	}
	stale.SandboxID = "sbx-22222222222222222222222222222222"
	if _, err = r.SaveExecution(stale, at); err == nil {
		t.Fatal("accepted stale ownership")
	}
	loaded, err := r.ListExecutions()
	if err != nil || len(loaded) != 1 || loaded[0].Receipt == nil {
		t.Fatalf("lost receipt: %#v %v", loaded, err)
	}
	if !reflect.DeepEqual(loaded[0].Inspection, e.Inspection) {
		t.Fatal("inspection history was not persisted")
	}
	e.Phase = "settled"
	e.Receipt.State = "succeeded"
	e, err = r.SaveExecution(e, at)
	if err != nil {
		t.Fatal(err)
	}
	value, err := r.Get(message.ID)
	if err != nil || value.WorkState != messagequeue.WorkAwaitingQuiescence {
		t.Fatalf("premature work completion: %#v %v", value, err)
	}
	e.Phase = "unavailable"
	e, err = r.SaveExecution(e, at)
	if err != nil {
		t.Fatal(err)
	}
	value, err = r.Get(message.ID)
	if err != nil || value.Status != messagequeue.StatusHandled || value.WorkState != messagequeue.WorkUnavailable {
		t.Fatalf("lost known handler outcome: %#v %v", value, err)
	}
}

func TestLostUnacknowledgedExecutionHasReadableUnknownOutcome(t *testing.T) {
	r, db, at := newMessageDeliveryRepository(t)
	defer db.Close()
	const sandbox = "sbx-11111111111111111111111111111111"
	if _, err := db.Exec("INSERT INTO sandboxes (id,state,record) VALUES (?,'running',X'01')", sandbox); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE sessions SET sandbox_id=? WHERE id='ses_actor_a'", sandbox); err != nil {
		t.Fatal(err)
	}
	m := queuedMessage("msg_00000000000000000000000000000001", "agent:a", at)
	_, _, _ = r.Enqueue(m)
	_, _, _ = r.ClaimNext(at)
	e, err := r.SaveExecution(messagequeue.Execution{MessageID: m.ID, Attempt: 1, SessionID: "ses_actor_a", SandboxID: sandbox, Phase: "sending", AdmissionDeadline: at.Add(30 * time.Second), ExecutionDeadline: at.Add(5 * time.Minute)}, at)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Requeue(m.ID, at); err == nil {
		t.Fatal("legacy mutation bypassed execution ownership")
	}
	e.Phase = "unavailable"
	e.ErrorCode = "sandbox_lost"
	if _, err = r.SaveExecution(e, at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	message, err := r.Get(m.ID)
	if err != nil || message.ErrorCode != "execution_outcome_unknown" {
		t.Fatalf("unknown outcome=%#v %v", message, err)
	}
	page, err := r.ListEvents(messagequeue.EventQuery{MessageID: m.ID, Limit: 10})
	if err != nil || len(page.Items) != 3 || page.Items[0].CurrentExecution.Phase != "unavailable" {
		t.Fatalf("history=%#v %v", page, err)
	}
}

func TestIntakeRedeliveryRequiresDeadOwnerAndFencesOldAttempt(t *testing.T) {
	r, db, at := newMessageDeliveryRepository(t)
	defer db.Close()
	const oldSandbox = "sbx-11111111111111111111111111111111"
	const newSandbox = "sbx-22222222222222222222222222222222"
	_, err := db.Exec("INSERT INTO sandboxes (id,state,record) VALUES (?,'running',X'01'),(?,'running',X'01')", oldSandbox, newSandbox)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("UPDATE sessions SET sandbox_id=? WHERE id='ses_actor_a'", oldSandbox)
	if err != nil {
		t.Fatal(err)
	}
	m := queuedMessage("msg_00000000000000000000000000000001", "agent:a", at)
	if _, _, err = r.Enqueue(m); err != nil {
		t.Fatal(err)
	}
	if _, _, err = r.ClaimNext(at); err != nil {
		t.Fatal(err)
	}
	e, err := r.SaveExecution(messagequeue.Execution{MessageID: m.ID, Attempt: 1, SessionID: "ses_actor_a", SandboxID: oldSandbox, Phase: "sending", RedeliverySupported: true, AdmissionDeadline: at.Add(30 * time.Second), ExecutionDeadline: at.Add(5 * time.Minute)}, at)
	if err != nil {
		t.Fatal(err)
	}
	stale := e
	e.Phase = "retrying"
	if _, err = r.SaveExecution(e, at); err == nil {
		t.Fatal("requeued live owner")
	}
	if _, err = db.Exec("UPDATE sandboxes SET state='stopped' WHERE id=?", oldSandbox); err != nil {
		t.Fatal(err)
	}
	beforeRetryCommit := e
	e, err = r.SaveExecution(e, at.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.SaveExecution(beforeRetryCommit, at.Add(time.Second)); err != nil {
		t.Fatal("lost requeue ACK is not idempotent", err)
	}
	m, claimed, err := r.ClaimNext(at.Add(3 * time.Second))
	if err != nil || !claimed || m.Attempts != 2 {
		t.Fatalf("not requeued: %#v %v", m, err)
	}
	if _, err = db.Exec("UPDATE sessions SET sandbox_id=? WHERE id='ses_actor_a'", newSandbox); err != nil {
		t.Fatal(err)
	}
	replacement := messagequeue.Execution{MessageID: m.ID, Attempt: 2, SessionID: e.SessionID, SandboxID: newSandbox, Phase: "sending", RedeliverySupported: true, AdmissionDeadline: at.Add(33 * time.Second), ExecutionDeadline: e.ExecutionDeadline}
	replacement, err = r.SaveExecution(replacement, at.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if replacement.Revision <= e.Revision {
		t.Fatal("replacement reset revision fence")
	}
	stale.Phase = "unavailable"
	if _, err = r.SaveExecution(stale, at.Add(4*time.Second)); err == nil {
		t.Fatal("old owner mutated replacement")
	}
	replacement.Phase = "settled"
	replacement.Receipt = &messageprotocol.Receipt{MessageID: m.ID, SandboxID: newSandbox, Sequence: 1, Generation: 1, AcceptedAt: at.Add(3 * time.Second), Deadline: e.ExecutionDeadline, State: "succeeded"}
	replacement, err = r.SaveExecution(replacement, at.Add(4*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	replacement.Phase = "retrying"
	if _, err = r.SaveExecution(replacement, at.Add(5*time.Second)); err == nil {
		t.Fatal("requeued acknowledged intake")
	}
}
