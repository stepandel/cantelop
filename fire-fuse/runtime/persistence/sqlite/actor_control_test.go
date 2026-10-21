package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/stepandel/cantelop/fire-fuse/runtime/messagequeue"
	"path/filepath"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/persistence/configcipher"
	"github.com/stepandel/cantelop/fire-fuse/runtime/persistence/sqliteschema"
	"github.com/stepandel/cantelop/fire-fuse/sessionevents"
)

func actorControlDB(t *testing.T) (*sql.DB, *configcipher.Cipher) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err = sqliteschema.Initialize(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	cipher, err := configcipher.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return db, cipher
}
func TestDurableViewAndReplayShareCommittedBoundary(t *testing.T) {
	db, cipher := actorControlDB(t)
	store := NewSessionEvents(db, cipher)
	key := sessionevents.Key{WorkspaceID: "wsp_0123456789abcdef0123456789abcdef", SessionID: "thread"}
	source := "sbx-0123456789abcdef0123456789abcdef"
	id := "msg_0123456789abcdef0123456789abcdef"
	if _, err := store.View(key); !errors.Is(err, sessionevents.ErrViewNotFound) {
		t.Fatalf("empty view: %v", err)
	}
	appendValue := func(cursor uint64, view *json.RawMessage) sessionevents.Event {
		value, err := store.Append(sessionevents.Append{Key: key, MessageID: id, SourceSandboxID: source, SourceCursor: cursor, Data: json.RawMessage(`{"delta":"hello"}`), View: view})
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := appendValue(1, nil)
	state := json.RawMessage(`{"text":"private state"}`)
	commit := appendValue(2, &state)
	after := appendValue(3, nil)
	var file string
	var seq int
	var name string
	if err := db.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &file); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = sqliteschema.Initialize(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	store = NewSessionEvents(db, cipher)
	restarted := NewSessionEvents(db, cipher)
	view, err := restarted.View(key)
	if err != nil || !bytes.Equal(view.State, state) || view.Cursor.StreamID != before.StreamID || view.Cursor.Sequence != commit.Sequence {
		t.Fatalf("view: %#v %v", view, err)
	}
	batch, err := restarted.ReadAvailable(key, view.Cursor.Sequence)
	if err != nil || len(batch.Events) != 1 || batch.Events[0].Sequence != after.Sequence {
		t.Fatalf("snapshot/stream gap: %#v %v", batch, err)
	}
	duplicate := appendValue(2, &state)
	if duplicate.Sequence != commit.Sequence {
		t.Fatal("duplicate view incremented revision")
	}
	if _, err := store.View(sessionevents.Key{WorkspaceID: key.WorkspaceID, SessionID: "other"}); !errors.Is(err, sessionevents.ErrViewNotFound) {
		t.Fatal("view leaked between Sessions")
	}
	var ciphertext []byte
	if err = db.QueryRow(`SELECT view_ciphertext FROM session_streams WHERE stream_id=?`, view.Cursor.StreamID).Scan(&ciphertext); err != nil || bytes.Contains(ciphertext, []byte("private state")) {
		t.Fatal("projection was not encrypted")
	}
	for i := 4; i < 270; i++ {
		appendValue(uint64(i), nil)
	}
	if _, err = store.ReadAvailable(key, 0); !errors.Is(err, sessionevents.ErrCursorExpired) {
		t.Fatalf("retention: %v", err)
	}
	// Metadata fingerprints retain deduplication after replay ciphertext expires.
	if duplicate := appendValue(2, &state); duplicate.Sequence != commit.Sequence {
		t.Fatal("retention lost source deduplication")
	}
	view, err = store.View(key)
	if err != nil || view.Revision != "2" {
		t.Fatal("retention removed committed projection")
	}
}

func TestUniformDeliveryAdvancesAfterAcknowledgmentWithoutWaitingForHandling(t *testing.T) {
	db, cipher := actorControlDB(t)
	at := time.Now().UTC()
	workspaceID := "wsp_0123456789abcdef0123456789abcdef"
	if _, err := db.Exec(`INSERT INTO apps(id,slug,created_at,updated_at)VALUES('app_0123456789abcdef0123456789abcdef','agent',?,?)`, formatTime(at), formatTime(at)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO workspaces(id,app_id,slug,created_at,updated_at)VALUES(?,'app_0123456789abcdef0123456789abcdef','production',?,?)`, workspaceID, formatTime(at), formatTime(at)); err != nil {
		t.Fatal(err)
	}

	store := NewMessageDeliveries(db, cipher)
	for i, kind := range []string{"dispatch", "steer", "cancel"} {
		id := fmt.Sprintf("msg_%032x", i+1)
		value := messagequeue.Message{ID: id, WorkspaceID: workspaceID, LogicalSessionID: "thread", KeepAliveSeconds: 900, Payload: json.RawMessage(`{"__cantelop_command":2,"type":"` + kind + `"}`), Status: messagequeue.StatusAccepted, WorkState: messagequeue.WorkPending, AcceptedAt: at.Add(time.Duration(i) * time.Millisecond), UpdatedAt: at.Add(time.Duration(i) * time.Millisecond)}
		if _, _, err := store.Enqueue(value); err != nil {
			t.Fatal(err)
		}
	}
	first, ok, err := store.ClaimNext(at.Add(time.Second))
	if err != nil || !ok || first.ID != "msg_00000000000000000000000000000001" {
		t.Fatal(first, err)
	}
	if _, ok, err = store.ClaimNext(at.Add(time.Second)); err != nil || ok {
		t.Fatal("delivery overtook an unacknowledged envelope")
	}
	for i := 1; i < 3; i++ {
		previous := fmt.Sprintf("msg_%032x", i)
		if _, err = db.Exec(`UPDATE message_deliveries SET admitted_at=? WHERE id=?`, formatTime(at.Add(time.Second)), previous); err != nil {
			t.Fatal(err)
		}
		next, found, err := store.ClaimNext(at.Add(2 * time.Second))
		if err != nil || !found || next.ID != fmt.Sprintf("msg_%032x", i+1) {
			t.Fatalf("command delivery: %#v %v", next, err)
		}
	}
	first, err = store.Get(first.ID)
	if err != nil || first.Status != messagequeue.StatusHandling {
		t.Fatal("handling completion was required to deliver subsequent envelopes")
	}
}

func TestProjectionsDoNotConsumeReplayWindowAndUnsourcedAppendsCommit(t *testing.T) {
	db, cipher := actorControlDB(t)
	store := NewSessionEvents(db, cipher)
	key := sessionevents.Key{WorkspaceID: "wsp_0123456789abcdef0123456789abcdef", SessionID: "thread"}
	source := "sbx-0123456789abcdef0123456789abcdef"
	id := "msg_0123456789abcdef0123456789abcdef"
	first, err := store.Append(sessionevents.Append{Key: key, MessageID: id, Data: json.RawMessage(`{"n":0}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Append(sessionevents.Append{Key: key, MessageID: id, Data: json.RawMessage(`{"n":1}`)}); err != nil {
		t.Fatalf("second unsourced append: %v", err)
	}
	state := json.RawMessage(`{"text":"view"}`)
	for cursor := uint64(1); cursor <= 300; cursor++ {
		if _, err = store.Append(sessionevents.Append{Key: key, MessageID: id, SourceSandboxID: source, SourceCursor: cursor, Data: json.RawMessage(`null`), View: &state}); err != nil {
			t.Fatal(err)
		}
	}
	batch, err := store.ReadAvailable(key, first.Sequence-1)
	if err != nil || len(batch.Events) != 2 || batch.Events[0].Sequence != first.Sequence {
		t.Fatalf("projections expired replay: %#v %v", batch, err)
	}
}

func TestStreamCapacityReclaimsViewStreams(t *testing.T) {
	db, cipher := actorControlDB(t)
	store := NewSessionEvents(db, cipher)
	source := "sbx-0123456789abcdef0123456789abcdef"
	id := "msg_0123456789abcdef0123456789abcdef"
	state := json.RawMessage(`{"text":"view"}`)
	for i := 0; i <= sessionevents.DefaultMaxStreams; i++ {
		key := sessionevents.Key{WorkspaceID: "wsp_0123456789abcdef0123456789abcdef", SessionID: fmt.Sprintf("thread-%d", i)}
		if _, err := store.Append(sessionevents.Append{Key: key, MessageID: id, SourceSandboxID: source, SourceCursor: 1, Data: json.RawMessage(`null`), View: &state}); err != nil {
			t.Fatalf("stream %d: %v", i, err)
		}
	}
	if _, err := store.View(sessionevents.Key{WorkspaceID: "wsp_0123456789abcdef0123456789abcdef", SessionID: "thread-0"}); !errors.Is(err, sessionevents.ErrViewNotFound) {
		t.Fatalf("oldest view was not reclaimed: %v", err)
	}
}
