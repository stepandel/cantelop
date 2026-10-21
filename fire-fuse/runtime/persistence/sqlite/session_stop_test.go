package sqlite

import (
	"errors"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/messageprotocol"
	"github.com/stepandel/cantelop/fire-fuse/runtime/messagequeue"
	"github.com/stepandel/cantelop/fire-fuse/runtime/model"
)

func TestSessionStopFencesRecoveryRedeliveryAndStaleAdmission(t *testing.T) {
	for _, phase := range []string{"sending", "settled", "retrying"} {
		t.Run(phase, func(t *testing.T) {
			r, db, at := newMessageDeliveryRepository(t)
			defer db.Close()
			const old = "sbx-11111111111111111111111111111111"
			const next = "sbx-22222222222222222222222222222222"
			if _, err := db.Exec("INSERT INTO sandboxes VALUES (?, 'running', X'01')", old); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("UPDATE sessions SET sandbox_id=? WHERE id='ses_actor_a'", old); err != nil {
				t.Fatal(err)
			}
			m := queuedMessage("msg_00000000000000000000000000000001", "agent:a", at)
			m.ReplyRequested = true
			if _, _, err := r.Enqueue(m); err != nil {
				t.Fatal(err)
			}
			if _, _, err := r.ClaimNext(at); err != nil {
				t.Fatal(err)
			}
			e, err := r.SaveExecution(messagequeue.Execution{MessageID: m.ID, Attempt: 1, SessionID: "ses_actor_a", SandboxID: old, Phase: "sending", RedeliverySupported: true, RecoverySupported: true, AdmissionDeadline: at.Add(30 * time.Second), ExecutionDeadline: at.Add(5 * time.Minute)}, at)
			if err != nil {
				t.Fatal(err)
			}
			if phase == "settled" {
				if _, err = r.SaveReply(m.ID, []byte(`{"retained":true}`), at); err != nil {
					t.Fatal(err)
				}

				e.Receipt = &messageprotocol.Receipt{SandboxID: old, MessageID: m.ID, Sequence: 1, Generation: 1, State: "succeeded", AcceptedAt: at, Deadline: e.ExecutionDeadline}
				e.Phase = messageprotocol.ExecutionPhase(phase)
				e, err = r.SaveExecution(e, at)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = r.SaveReply(m.ID, []byte(`{"retained":true}`), at); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "retrying" {
				if _, err = db.Exec("UPDATE sandboxes SET state='terminated' WHERE id=?", old); err != nil {
					t.Fatal(err)
				}
				e.Phase = messageprotocol.ExecutionPhase(phase)
				e, err = r.SaveExecution(e, at)
				if err != nil {
					t.Fatal(err)
				}
			}
			mutation := func(b SandboxRecord) (SandboxRecord, time.Time, error) {
				b.State = model.StateStopping
				return b, at, nil
			}
			if err = r.FenceSessionStop("ses_actor_a", old, mutation, at); err != nil {
				t.Fatal(err)
			}
			if err = r.FenceSessionStop("ses_actor_a", old, mutation, at); err != nil {
				t.Fatalf("repeat stop: %v", err)
			}
			if _, err = r.SaveExecution(e, at); !errors.Is(err, messagequeue.ErrConflict) {
				t.Fatalf("stale execution save: %v", err)
			}
			if active, err := r.ListExecutions(); err != nil || len(active) != 0 {
				t.Fatalf("active executions: %v %v", active, err)
			}
			stored, err := r.Get(m.ID)
			if err != nil || stored.WorkState != messagequeue.WorkUnavailable {
				t.Fatalf("stopped outcome: %#v %v", stored, err)
			}
			if phase == "settled" && (stored.Status != messagequeue.StatusHandled || string(stored.Reply) != `{"retained":true}`) {
				t.Fatalf("lost ACK or reply: %#v", stored)
			}
			if phase != "settled" && stored.Status != messagequeue.StatusFailed {
				t.Fatalf("unacknowledged stop: %#v", stored)
			}
			if _, claimed, err := r.ClaimNext(at); err != nil || claimed {
				t.Fatalf("stopped retry claimed: %v %v", claimed, err)
			}
			// A new message cannot bind to the stopped allocation, but can bind
			// to the fresh allocation of the same reusable public Session.
			fresh := queuedMessage("msg_00000000000000000000000000000002", "agent:a", at)
			if _, _, err = r.Enqueue(fresh); err != nil {
				t.Fatal(err)
			}
			if _, _, err = r.ClaimNext(at); err != nil {
				t.Fatal(err)
			}
			newExecution := messagequeue.Execution{MessageID: fresh.ID, Attempt: 1, SessionID: "ses_actor_a", SandboxID: old, Phase: "sending", AdmissionDeadline: e.AdmissionDeadline, ExecutionDeadline: e.ExecutionDeadline}
			if _, err = r.SaveExecution(newExecution, at); !errors.Is(err, messagequeue.ErrConflict) {
				t.Fatalf("stopped admission: %v", err)
			}
			if _, err = db.Exec("INSERT INTO sandboxes VALUES (?, 'running', X'02')", next); err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec("UPDATE sessions SET sandbox_id=? WHERE id='ses_actor_a'", next); err != nil {
				t.Fatal(err)
			}
			newExecution.SandboxID = next
			if _, err = r.SaveExecution(newExecution, at); err != nil {
				t.Fatalf("fresh activation: %v", err)
			}
			if err = r.FenceSessionStop("ses_actor_a", old, mutation, at); !errors.Is(err, messagequeue.ErrConflict) {
				t.Fatalf("old stop affected replacement: %v", err)
			}
		})
	}
}
