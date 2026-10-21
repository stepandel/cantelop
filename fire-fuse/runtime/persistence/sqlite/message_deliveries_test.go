package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/messagequeue"
	"github.com/stepandel/cantelop/fire-fuse/runtime/persistence/configcipher"
	"github.com/stepandel/cantelop/fire-fuse/runtime/persistence/sqliteschema"
)

func TestMessageDeliveriesPersistEncryptedIdempotentAcceptance(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := sqliteschema.Initialize(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	for _, statement := range []string{
		`INSERT INTO compute_tenants VALUES ('tenant-1', '` + at.Format(time.RFC3339Nano) + `')`,
		`INSERT INTO apps (id, slug, created_at, updated_at) VALUES ('app_0123456789abcdef0123456789abcdef', 'agent', '` + at.Format(time.RFC3339Nano) + `', '` + at.Format(time.RFC3339Nano) + `')`,
		`INSERT INTO workspaces (id, app_id, slug, created_at, updated_at) VALUES ('wsp_0123456789abcdef0123456789abcdef', 'app_0123456789abcdef0123456789abcdef', 'production', '` + at.Format(time.RFC3339Nano) + `', '` + at.Format(time.RFC3339Nano) + `')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	cipher, err := configcipher.New(bytes.Repeat([]byte{7}, configcipher.KeyBytes))
	if err != nil {
		t.Fatal(err)
	}
	repository := NewMessageDeliveries(db, cipher)
	accepted := messagequeue.Message{
		ID:               "msg_0123456789abcdef0123456789abcdef",
		WorkspaceID:      "wsp_0123456789abcdef0123456789abcdef",
		LogicalSessionID: "agent:primary", RequestID: "req_0123456789abcdef0123456789abcdef",
		KeepAliveSeconds: 300,
		Payload:          json.RawMessage(`{"token":"private"}`), Status: messagequeue.StatusAccepted,
		ReplyRequested: true,
		WorkState:      messagequeue.WorkPending, AcceptedAt: at, UpdatedAt: at,
	}
	stored, created, err := repository.Enqueue(accepted)
	if err != nil || !created || stored.ID != accepted.ID {
		t.Fatalf("Enqueue() = %#v, %t, %v", stored, created, err)
	}
	if _, created, err := repository.Enqueue(accepted); err != nil || created {
		t.Fatalf("idempotent Enqueue() created=%t err=%v", created, err)
	}
	limited := accepted
	limited.ID = "msg_11111111111111111111111111111111"
	repository.maxAppRows = 1
	if _, _, err := repository.Enqueue(limited); !errors.Is(err, messagequeue.ErrStorageQuota) {
		t.Fatalf("App row quota error = %v", err)
	}
	if _, created, err := repository.Enqueue(accepted); err != nil || created {
		t.Fatalf("replay at capacity created=%t err=%v", created, err)
	}
	repository.maxAppRows = 20_000
	repository.maxHostBytes = 1
	if _, _, err := repository.Enqueue(limited); !errors.Is(err, messagequeue.ErrStorageQuota) {
		t.Fatalf("host byte quota error = %v", err)
	}
	repository.maxHostBytes = 1 << 30
	retried := accepted
	retried.RequestID = "req_11111111111111111111111111111111"
	if _, created, err := repository.Enqueue(retried); err != nil || created {
		t.Fatalf("request retry with new trace identity created=%t err=%v", created, err)
	}
	changed := accepted
	changed.KeepAliveSeconds++
	if _, _, err := repository.Enqueue(changed); !errors.Is(err, messagequeue.ErrConflict) {
		t.Fatalf("conflicting Enqueue() = %v", err)
	}
	var ciphertext []byte
	if err := db.QueryRow("SELECT payload_ciphertext FROM message_deliveries WHERE id = ?", accepted.ID).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte("private")) {
		t.Fatal("stored Message payload contains plaintext")
	}
	if _, err := repository.SaveReply(accepted.ID, json.RawMessage(`{"authenticated":true}`), at.Add(time.Second)); !errors.Is(err, messagequeue.ErrConflict) {
		t.Fatalf("SaveReply() before handling = %v, want ErrConflict", err)
	}
	if _, claimed, err := repository.ClaimNext(at.Add(time.Second)); err != nil || !claimed {
		t.Fatalf("ClaimNext() claimed=%t err=%v", claimed, err)
	}
	for range 2 {
		if _, err := repository.SaveReply(accepted.ID, json.RawMessage(`{"authenticated":true}`), at.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	var replyCiphertext []byte
	if err := db.QueryRow("SELECT reply_ciphertext FROM message_deliveries WHERE id = ?", accepted.ID).Scan(&replyCiphertext); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(replyCiphertext, []byte("authenticated")) {
		t.Fatal("stored Message reply contains plaintext")
	}
	loaded, err := repository.Get(accepted.ID)
	if err != nil || string(loaded.Payload) != string(accepted.Payload) || loaded.RequestID != accepted.RequestID || string(loaded.Reply) != `{"authenticated":true}` {
		t.Fatalf("Get() = %#v, %v", loaded, err)
	}
}
