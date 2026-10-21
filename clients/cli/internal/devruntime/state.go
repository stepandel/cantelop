package devruntime

import (
	"crypto/rand"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/stepandel/cantelop/clients/cli/internal/localhttp"
	"github.com/stepandel/cantelop/fire-fuse/runtime/persistence/configcipher"
	sqlitepersistence "github.com/stepandel/cantelop/fire-fuse/runtime/persistence/sqlite"
	"github.com/stepandel/cantelop/fire-fuse/sessionevents"
)

func (runtime *localRuntime) enableDurableEvents(root string) (func(), error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	keyPath := filepath.Join(root, "state.key")
	key, err := os.ReadFile(keyPath)
	if errors.Is(err, os.ErrNotExist) {
		key = make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return nil, err
		}
		file, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return nil, err
		}
		_, err = file.Write(key)
		closeErr := file.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	cipher, err := configcipher.New(key)
	if err != nil {
		return nil, err
	}
	db, err := sqlitepersistence.Open(filepath.Join(root, "state.db"))
	if err != nil {
		return nil, err
	}
	runtime.events = sqlitepersistence.NewSessionEvents(db.SQL(), cipher)
	// Workspace IDs are derived from App and slug, but retaining the directory
	// also permits ID-based view reads immediately after restarting dev.
	rows, err := db.SQL().Query(`SELECT id,slug,created_at FROM workspaces`)
	if err != nil {
		db.Close()
		return nil, err
	}
	for rows.Next() {
		var workspace localWorkspace
		var at string
		if err = rows.Scan(&workspace.ID, &workspace.Slug, &at); err != nil {
			rows.Close()
			db.Close()
			return nil, err
		}
		workspace.CreatedAt, err = time.Parse(time.RFC3339Nano, at)
		if err != nil {
			rows.Close()
			db.Close()
			return nil, err
		}
		runtime.workspaces[workspace.ID] = workspace
		runtime.slugs[workspace.Slug] = workspace.ID
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		db.Close()
		return nil, err
	}
	runtime.persistWorkspace = func(value localWorkspace) error {
		at := value.CreatedAt.Format("2006-01-02T15:04:05.999999999Z07:00")
		_, err := db.SQL().Exec(`INSERT OR IGNORE INTO apps(id,slug,created_at,updated_at) VALUES(?,?,?,?)`, runtime.appID, "local", at, at)
		if err != nil {
			return err
		}
		_, err = db.SQL().Exec(`INSERT OR IGNORE INTO workspaces(id,app_id,slug,created_at,updated_at)VALUES(?,?,?,?,?)`, value.ID, runtime.appID, value.Slug, at, at)
		return err
	}
	return func() { _ = db.Close() }, nil
}

func (runtime *localRuntime) integrationView(w http.ResponseWriter, r *http.Request, workspaceID, sessionID string) {
	store, ok := runtime.events.(interface {
		View(sessionevents.Key) (sessionevents.View, error)
	})
	if !ok {
		localhttp.WriteError(w, 503, "session_view_unavailable")
		return
	}
	value, err := store.View(sessionevents.Key{WorkspaceID: workspaceID, SessionID: sessionID})
	if errors.Is(err, sessionevents.ErrViewNotFound) {
		localhttp.WriteError(w, 404, "session_view_not_found")
		return
	}
	if err != nil {
		localhttp.WriteError(w, 503, "session_view_unavailable")
		return
	}
	localhttp.WriteJSON(w, 200, map[string]any{"workspaceId": workspaceID, "sessionId": sessionID, "revision": value.Revision, "updatedAt": value.UpdatedAt, "state": value.State, "cursor": value.Cursor})
}
