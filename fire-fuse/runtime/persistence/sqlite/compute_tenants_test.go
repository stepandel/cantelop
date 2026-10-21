package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/model"
	"github.com/stepandel/cantelop/fire-fuse/runtime/persistence/sqliteschema"
	_ "modernc.org/sqlite"
)

func TestComputeTenantsEnsureAndGet(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := sqliteschema.Initialize(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	repository := NewComputeTenants(db)
	want := model.ComputeTenant{ID: "usr_alice", CreatedAt: time.Now().UTC()}
	created, err := repository.Ensure(want)
	if err != nil || !created {
		t.Fatalf("Ensure() = %t, %v", created, err)
	}
	created, err = repository.Ensure(want)
	if err != nil || created {
		t.Fatalf("Ensure() replay = %t, %v", created, err)
	}
	got, err := repository.Get(want.ID)
	if err != nil || got != want {
		t.Fatalf("Get() = %#v, %v", got, err)
	}
	if _, err := repository.Get("usr_missing"); !errors.Is(err, model.ErrComputeTenantMissing) {
		t.Fatalf("Get(missing) = %v", err)
	}
}
