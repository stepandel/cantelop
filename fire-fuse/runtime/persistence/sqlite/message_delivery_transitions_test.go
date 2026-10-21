package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/messagequeue"
	"github.com/stepandel/cantelop/fire-fuse/runtime/persistence/configcipher"
	"github.com/stepandel/cantelop/fire-fuse/runtime/persistence/sqliteschema"
)

func TestMessageDeliveryTransitionsPreservePerSessionOrder(t *testing.T) {
	repository, db, at := newMessageDeliveryRepository(t)
	defer db.Close()
	first := queuedMessage("msg_00000000000000000000000000000001", "agent:a", at)
	second := queuedMessage("msg_00000000000000000000000000000002", "agent:a", at.Add(time.Second))
	other := queuedMessage("msg_00000000000000000000000000000003", "agent:b", at.Add(2*time.Second))
	for _, value := range []messagequeue.Message{first, second, other} {
		if _, _, err := repository.Enqueue(value); err != nil {
			t.Fatal(err)
		}
	}
	claimed, found, err := repository.ClaimNext(at.Add(3 * time.Second))
	if err != nil || !found || claimed.ID != first.ID || claimed.Attempts != 1 {
		t.Fatalf("first ClaimNext() = %#v, %t, %v", claimed, found, err)
	}
	claimed, found, err = repository.ClaimNext(at.Add(4 * time.Second))
	if err != nil || !found || claimed.ID != other.ID {
		t.Fatalf("parallel Session ClaimNext() = %#v, %t, %v", claimed, found, err)
	}
	if _, found, err := repository.ClaimNext(at.Add(5 * time.Second)); err != nil || found {
		t.Fatalf("blocked ClaimNext() found=%t err=%v", found, err)
	}
	if _, err := repository.Fail(first.ID, "ses_actor_a", "message_handler_failed", at.Add(6*time.Second)); err != nil {
		t.Fatal(err)
	}
	claimed, found, err = repository.ClaimNext(at.Add(7 * time.Second))
	if err != nil || !found || claimed.ID != second.ID {
		t.Fatalf("post-failure ClaimNext() = %#v, %t, %v", claimed, found, err)
	}
	if _, found, err := repository.ClaimNext(at.Add(8 * time.Second)); err != nil || found {
		t.Fatalf("handling successor ClaimNext() found=%t err=%v", found, err)
	}
}

func TestMessageAdmissionBackoffSurvivesRepositoryRestartAndPreservesOrder(t *testing.T) {
	r, db, at := newMessageDeliveryRepository(t)
	defer db.Close()
	first := queuedMessage("msg_00000000000000000000000000000001", "agent:a", at)
	second := queuedMessage("msg_00000000000000000000000000000002", "agent:a", at.Add(time.Millisecond))
	other := queuedMessage("msg_00000000000000000000000000000003", "agent:b", at.Add(2*time.Millisecond))
	for _, m := range []messagequeue.Message{first, second, other} {
		if _, _, err := r.Enqueue(m); err != nil {
			t.Fatal(err)
		}
	}
	m, found, err := r.ClaimNext(at)
	if err != nil || !found || m.ID != first.ID {
		t.Fatalf("first claim: %v %v %v", m, found, err)
	}
	if _, err := r.Requeue(first.ID, at); err != nil {
		t.Fatal(err)
	}
	// Other sessions progress while the oldest message holds its place in its
	// own session. Recreating the repository must not reset the persisted delay.
	r = NewMessageDeliveries(db, r.cipher)
	m, found, err = r.ClaimNext(at.Add(100 * time.Millisecond))
	if err != nil || !found || m.ID != other.ID {
		t.Fatalf("independent claim: %v %v %v", m, found, err)
	}
	for attempt, seconds := range []int{1, 3, 7, 15, 23, 30} {
		next := at.Add(time.Duration(seconds) * time.Second)
		if _, found, err := r.ClaimNext(next.Add(-10 * time.Millisecond)); err != nil || found {
			t.Fatalf("retry %d claimed early: found=%v err=%v", attempt, found, err)
		}
		m, found, err = r.ClaimNext(next.Add(time.Millisecond))
		if err != nil || !found || m.ID != first.ID || m.Attempts != attempt+2 {
			t.Fatalf("retry %d: %v %v %v", attempt, m, found, err)
		}
		if seconds == 30 {
			if _, err := r.Fail(m.ID, "", "session_admission_timeout", next.Add(time.Millisecond)); err != nil {
				t.Fatal(err)
			}
		} else if _, err := r.Requeue(m.ID, next); err != nil {
			t.Fatal(err)
		}
	}
	m, found, err = r.ClaimNext(at.Add(31 * time.Second))
	if err != nil || !found || m.ID != second.ID {
		t.Fatalf("successor: %v %v %v", m, found, err)
	}
}

func TestMessageDeliveryRecoveryRequeuesHandlingWork(t *testing.T) {
	repository, db, at := newMessageDeliveryRepository(t)
	defer db.Close()
	value := queuedMessage("msg_00000000000000000000000000000001", "agent:a", at)
	if _, _, err := repository.Enqueue(value); err != nil {
		t.Fatal(err)
	}
	if _, found, err := repository.ClaimNext(at.Add(time.Second)); err != nil || !found {
		t.Fatalf("ClaimNext() found=%t err=%v", found, err)
	}
	if count, err := repository.RequeueHandling(at.Add(2 * time.Second)); err != nil || count != 1 {
		t.Fatalf("RequeueHandling() = %d, %v", count, err)
	}
	claimed, found, err := repository.ClaimNext(at.Add(3 * time.Second))
	if err != nil || !found || claimed.Attempts != 2 {
		t.Fatalf("recovered ClaimNext() = %#v, %t, %v", claimed, found, err)
	}
	failed, err := repository.Fail(value.ID, "ses_actor_a", "message_handler_failed", at.Add(4*time.Second))
	if err != nil || failed.Status != messagequeue.StatusFailed || failed.WorkState != messagequeue.WorkUnavailable {
		t.Fatalf("Fail() = %#v, %v", failed, err)
	}
	wantTransitions := []string{"accepted", "handling", "requeued", "handling", "failed"}
	if transitions := messageTransitions(t, db, value.ID); !reflect.DeepEqual(transitions, wantTransitions) {
		t.Fatalf("transitions = %#v, want %#v", transitions, wantTransitions)
	}
}

func TestMessageDeliveryHistoryIsPayloadFreeAndCorrelated(t *testing.T) {
	repository, db, at := newMessageDeliveryRepository(t)
	defer db.Close()
	value := queuedMessage("msg_00000000000000000000000000000001", "agent:a", at)
	value.RequestID = "req_0123456789abcdef0123456789abcdef"
	if _, _, err := repository.Enqueue(value); err != nil {
		t.Fatal(err)
	}
	if _, found, err := repository.ClaimNext(at.Add(time.Second)); err != nil || !found {
		t.Fatalf("ClaimNext() found=%t err=%v", found, err)
	}
	if _, err := repository.Fail(value.ID, "ses_actor_a", "message_handler_failed", at.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if transitions := messageTransitions(t, db, value.ID); !reflect.DeepEqual(
		transitions, []string{"accepted", "handling", "failed"},
	) {
		t.Fatalf("transitions = %#v", transitions)
	}
	var requestID, sessionID string
	if err := db.QueryRow(`SELECT request_id, session_id FROM message_delivery_events
WHERE message_id = ? AND transition = 'failed'`, value.ID).Scan(&requestID, &sessionID); err != nil {
		t.Fatal(err)
	}
	if requestID != value.RequestID || sessionID != "ses_actor_a" {
		t.Fatalf("correlation = %q/%q", requestID, sessionID)
	}
	var definition string
	if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = 'message_delivery_events'`).Scan(&definition); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(definition, "payload") {
		t.Fatal("Message delivery event schema contains payload data")
	}
}

func TestMessageProjectionRollsBackWhenEventAppendFails(t *testing.T) {
	repository, db, at := newMessageDeliveryRepository(t)
	defer db.Close()
	value := queuedMessage("msg_00000000000000000000000000000001", "agent:a", at)
	if _, _, err := repository.Enqueue(value); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER reject_handling_event
BEFORE INSERT ON message_delivery_events WHEN NEW.transition = 'handling'
BEGIN SELECT RAISE(ABORT, 'rejected event'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.ClaimNext(at.Add(time.Second)); err == nil {
		t.Fatal("ClaimNext() succeeded without its lifecycle event")
	}
	stored, err := repository.Get(value.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != messagequeue.StatusAccepted || stored.WorkState != messagequeue.WorkPending || stored.Attempts != 0 {
		t.Fatalf("rolled-back Message = %#v", stored)
	}
	if transitions := messageTransitions(t, db, value.ID); !reflect.DeepEqual(transitions, []string{"accepted"}) {
		t.Fatalf("transitions after rollback = %#v", transitions)
	}
}

func TestMessageDeliveryEventPaginationAndTerminalRetention(t *testing.T) {
	repository, db, at := newMessageDeliveryRepository(t)
	defer db.Close()
	terminal := queuedMessage("msg_00000000000000000000000000000001", "agent:a", at)
	if _, _, err := repository.Enqueue(terminal); err != nil {
		t.Fatal(err)
	}
	if _, found, err := repository.ClaimNext(at.Add(time.Second)); err != nil || !found {
		t.Fatalf("ClaimNext() found=%t err=%v", found, err)
	}
	if _, err := repository.Fail(terminal.ID, "ses_actor_a", "message_handler_failed", at.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	active := queuedMessage("msg_00000000000000000000000000000002", "agent:b", at.Add(4*time.Second))
	if _, _, err := repository.Enqueue(active); err != nil {
		t.Fatal(err)
	}

	query := messagequeue.EventQuery{WorkspaceID: terminal.WorkspaceID, Limit: 2}
	first, err := repository.ListEvents(query)
	if err != nil || len(first.Items) != 2 || first.Items[0].MessageID != active.ID || first.NextBeforeSequence == 0 {
		t.Fatalf("first event page = %#v, %v", first, err)
	}
	query.BeforeSequence = first.NextBeforeSequence
	second, err := repository.ListEvents(query)
	if err != nil || len(second.Items) != 2 || second.NextBeforeSequence != 0 {
		t.Fatalf("second event page = %#v, %v", second, err)
	}
	messagePage, err := repository.ListEvents(messagequeue.EventQuery{MessageID: terminal.ID, Limit: 10})
	if err != nil || len(messagePage.Items) != 3 || messagePage.Items[0].Transition != messagequeue.TransitionFailed {
		t.Fatalf("Message event page = %#v, %v", messagePage, err)
	}

	pruned, err := repository.PruneTerminalEventsBefore(at.Add(4 * time.Second))
	if err != nil || pruned != 3 {
		t.Fatalf("PruneTerminalEventsBefore() = %d, %v", pruned, err)
	}
	if events := messageTransitions(t, db, terminal.ID); len(events) != 0 {
		t.Fatalf("retained terminal events = %#v", events)
	}
	if events := messageTransitions(t, db, active.ID); !reflect.DeepEqual(events, []string{"accepted"}) {
		t.Fatalf("active events = %#v", events)
	}
	if stored, err := repository.Get(terminal.ID); err != nil || stored.WorkState != messagequeue.WorkUnavailable {
		t.Fatalf("retained terminal projection = %#v, %v", stored, err)
	}
}

func TestMessageAcceptanceAppliesThirtyDayTerminalEventRetention(t *testing.T) {
	repository, db, at := newMessageDeliveryRepository(t)
	defer db.Close()
	terminal := queuedMessage("msg_00000000000000000000000000000001", "agent:a", at)
	if _, _, err := repository.Enqueue(terminal); err != nil {
		t.Fatal(err)
	}
	if _, found, err := repository.ClaimNext(at.Add(time.Second)); err != nil || !found {
		t.Fatalf("ClaimNext() found=%t err=%v", found, err)
	}
	if _, err := repository.Fail(terminal.ID, "ses_actor_a", "message_handler_failed", at.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	newest := queuedMessage(
		"msg_00000000000000000000000000000002",
		"agent:b",
		at.Add(messageDeliveryEventRetention+4*time.Second),
	)
	if _, _, err := repository.Enqueue(newest); err != nil {
		t.Fatal(err)
	}
	if events := messageTransitions(t, db, terminal.ID); len(events) != 0 {
		t.Fatalf("expired terminal events = %#v", events)
	}
	if events := messageTransitions(t, db, newest.ID); !reflect.DeepEqual(events, []string{"accepted"}) {
		t.Fatalf("new Message events = %#v", events)
	}
}

func messageTransitions(t *testing.T, db *sql.DB, messageID string) []string {
	t.Helper()
	rows, err := db.Query(`SELECT transition FROM message_delivery_events WHERE message_id = ? ORDER BY sequence`, messageID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var transitions []string
	for rows.Next() {
		var transition string
		if err := rows.Scan(&transition); err != nil {
			t.Fatal(err)
		}
		transitions = append(transitions, transition)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return transitions
}

func newMessageDeliveryRepository(t *testing.T) (*MessageDeliveries, *sql.DB, time.Time) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := sqliteschema.Initialize(context.Background(), db); err != nil {
		db.Close()
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	for _, statement := range []string{
		`INSERT INTO compute_tenants VALUES ('tenant-1', '` + at.Format(time.RFC3339Nano) + `')`,
		`INSERT INTO apps (id, slug, created_at, updated_at) VALUES ('app_0123456789abcdef0123456789abcdef', 'agent', '` + at.Format(time.RFC3339Nano) + `', '` + at.Format(time.RFC3339Nano) + `')`,
		`INSERT INTO workspaces (id, app_id, slug, created_at, updated_at) VALUES ('wsp_0123456789abcdef0123456789abcdef', 'app_0123456789abcdef0123456789abcdef', 'production', '` + at.Format(time.RFC3339Nano) + `', '` + at.Format(time.RFC3339Nano) + `')`,
		`INSERT INTO sandboxes (id, state, record) VALUES ('sbx_a', 'running', X'01'), ('sbx_b', 'running', X'02')`,
		`INSERT INTO sessions (id, workspace_id, build_id, sandbox_id, created_at, updated_at, keep_alive_seconds) VALUES
('ses_actor_a', 'wsp_0123456789abcdef0123456789abcdef', 'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 'sbx_a', '` + at.Format(time.RFC3339Nano) + `', '` + at.Format(time.RFC3339Nano) + `', 300),
('ses_actor_b', 'wsp_0123456789abcdef0123456789abcdef', 'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 'sbx_b', '` + at.Format(time.RFC3339Nano) + `', '` + at.Format(time.RFC3339Nano) + `', 300)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	cipher, err := configcipher.New(bytes.Repeat([]byte{7}, configcipher.KeyBytes))
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	return NewMessageDeliveries(db, cipher), db, at
}

func queuedMessage(id, logicalSessionID string, at time.Time) messagequeue.Message {
	return messagequeue.Message{
		ID: id, WorkspaceID: "wsp_0123456789abcdef0123456789abcdef",
		LogicalSessionID: logicalSessionID, KeepAliveSeconds: 300,
		Payload: json.RawMessage(`{"type":"prompt"}`), Status: messagequeue.StatusAccepted,
		WorkState: messagequeue.WorkPending, AcceptedAt: at, UpdatedAt: at,
	}
}
