package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/stepandel/cantelop/fire-fuse/controlclient"
	"github.com/stepandel/cantelop/fire-fuse/runtime/persistence/configcipher"
	sqlitepersistence "github.com/stepandel/cantelop/fire-fuse/runtime/persistence/sqlite"
	"github.com/stepandel/cantelop/fire-fuse/runtime/persistence/sqliteschema"
	"github.com/stepandel/cantelop/fire-fuse/sessionevents"
)

type durableEventControl struct{ store sessionevents.Store }

func (c durableEventControl) SessionEventStore() sessionevents.Store { return c.store }
func TestControlClientCommitsAndReadsDurableProjection(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = sqliteschema.Initialize(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	cipher, err := configcipher.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(durableEventControl{sqlitepersistence.NewSessionEvents(db, cipher)})
	client, err := controlclient.New("http://127.0.0.1:1", &http.Client{Transport: sessionEventRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Result(), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	state := json.RawMessage(`{"text":"durable"}`)
	input := controlclient.SessionEventAppend{WorkspaceID: "wsp_0123456789abcdef0123456789abcdef", SessionID: "thread", MessageID: "msg_0123456789abcdef0123456789abcdef", SourceSandboxID: "sbx-0123456789abcdef0123456789abcdef", SourceCursor: 1, Data: json.RawMessage(`null`), View: &state}
	committed, err := client.AppendSessionEvent(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	handler = NewHandler(durableEventControl{sqlitepersistence.NewSessionEvents(db, cipher)})
	view, err := client.SessionView(context.Background(), input.WorkspaceID, input.SessionID)
	if err != nil || view.Cursor.Sequence != committed.Sequence || !bytes.Equal(view.State, state) {
		t.Fatalf("view: %#v %v", view, err)
	}
	replay, err := client.ReadSessionEvents(context.Background(), input.WorkspaceID, input.SessionID, 0, false)
	if err != nil || len(replay.Events) != 0 || replay.LatestSequence != committed.Sequence {
		t.Fatalf("projection appeared as application output: %#v %v", replay, err)
	}
}
