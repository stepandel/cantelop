package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/model"
	"github.com/stepandel/cantelop/fire-fuse/runtime/persistence/sqliteschema"
	_ "modernc.org/sqlite"
)

func openOpaqueRecordDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := sqliteschema.Initialize(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestOpaqueRecordMutationsSkipUnchangedWrites(t *testing.T) {
	db := openOpaqueRecordDB(t)
	slots := NewHostSlotRecords(db)
	sandboxes := NewSandboxRecords(db)
	if created, err := slots.Create(HostSlotRecord{ID: "slot_a", State: "ready", Record: []byte(`{}`)}); err != nil || !created {
		t.Fatalf("Create(slot) = %t, %v", created, err)
	}
	if created, err := sandboxes.Create(SandboxRecord{ID: "sbx_a", State: model.StateRunning, Record: []byte(`{}`)}); err != nil || !created {
		t.Fatalf("Create(sandbox) = %t, %v", created, err)
	}
	if _, err := db.Exec(`
CREATE TRIGGER host_slots_reject_write BEFORE UPDATE ON host_slots BEGIN SELECT RAISE(ABORT, 'unexpected write'); END;
CREATE TRIGGER sandboxes_reject_write BEFORE UPDATE ON sandboxes BEGIN SELECT RAISE(ABORT, 'unexpected write'); END;`); err != nil {
		t.Fatal(err)
	}

	slot, err := slots.Mutate("slot_a", func(current HostSlotRecord) (HostSlotRecord, error) {
		return HostSlotRecord{ID: current.ID, State: current.State, Record: []byte(`{}`)}, nil
	})
	if err != nil || slot.State != "ready" {
		t.Fatalf("unchanged host slot Mutate() = %#v, %v", slot, err)
	}
	sandbox, err := sandboxes.Mutate("sbx_a", func(current SandboxRecord) (SandboxRecord, time.Time, error) {
		return current, time.Now(), nil
	})
	if err != nil || sandbox.State != model.StateRunning {
		t.Fatalf("unchanged sandbox Mutate() = %#v, %v", sandbox, err)
	}
	if _, err := slots.Mutate("slot_a", func(current HostSlotRecord) (HostSlotRecord, error) {
		current.State = "draining"
		return current, nil
	}); err == nil || !strings.Contains(err.Error(), "unexpected write") {
		t.Fatalf("changed host slot Mutate() = %v, want write attempt", err)
	}
}

func TestSandboxRecordsListActiveOmitsTerminated(t *testing.T) {
	db := openOpaqueRecordDB(t)
	sandboxes := NewSandboxRecords(db)
	for _, record := range []SandboxRecord{
		{ID: "sbx_c", State: model.StateFailed, Record: []byte(`{}`)},
		{ID: "sbx_b", State: model.StateTerminated, Record: []byte(`{}`)},
		{ID: "sbx_a", State: model.StateRunning, Record: []byte(`{}`)},
	} {
		if _, err := sandboxes.Create(record); err != nil {
			t.Fatal(err)
		}
	}
	active, err := sandboxes.ListActive()
	if err != nil || len(active) != 2 || active[0].ID != "sbx_a" || active[1].ID != "sbx_c" {
		t.Fatalf("ListActive() = %#v, %v", active, err)
	}
	all, err := sandboxes.List()
	if err != nil || len(all) != 3 {
		t.Fatalf("List() = %#v, %v", all, err)
	}
}

func TestHostSlotClaimMovesSlotIntoSandbox(t *testing.T) {
	db := openOpaqueRecordDB(t)
	slots := NewHostSlotRecords(db)
	sandboxes := NewSandboxRecords(db)
	if _, err := slots.Create(HostSlotRecord{ID: "slot_a", State: "ready", Record: []byte(`{"slot":true}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := sandboxes.Create(SandboxRecord{ID: "sbx_a", State: model.StateStarting, Record: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}

	if _, err := slots.Claim("sbx_a", "slot_a", func(sandbox SandboxRecord, _ HostSlotRecord) (SandboxRecord, error) {
		sandbox.ID = "sbx_other"
		return sandbox, nil
	}); err == nil {
		t.Fatal("Claim() accepted a record for another sandbox")
	}
	if _, err := slots.Claim("sbx_missing", "slot_a", func(sandbox SandboxRecord, _ HostSlotRecord) (SandboxRecord, error) {
		return sandbox, nil
	}); !errors.Is(err, ErrSandboxRecordNotFound) {
		t.Fatalf("Claim(missing sandbox) = %v", err)
	}

	claimed, err := slots.Claim("sbx_a", "slot_a", func(sandbox SandboxRecord, slot HostSlotRecord) (SandboxRecord, error) {
		sandbox.Record = slot.Record
		return sandbox, nil
	})
	if err != nil || string(claimed.Record) != `{"slot":true}` {
		t.Fatalf("Claim() = %#v, %v", claimed, err)
	}
	stored, err := sandboxes.Get("sbx_a")
	if err != nil || string(stored.Record) != `{"slot":true}` {
		t.Fatalf("Get() after claim = %#v, %v", stored, err)
	}
	if remaining, err := slots.List(); err != nil || len(remaining) != 0 {
		t.Fatalf("List() after claim = %#v, %v", remaining, err)
	}
}
