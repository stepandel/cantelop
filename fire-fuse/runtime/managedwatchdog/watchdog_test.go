package managedwatchdog

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWatchdogEnforcesDeadlinesWithoutRuntimeProcess(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE managed_apps(app_id TEXT,record BLOB);CREATE TABLE sandboxes(id TEXT,state TEXT,record BLOB);`)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	state := microvm.ManagedState{ManagedProjection: microvm.ManagedProjection{Policy: microvm.ManagedPolicy{Active: true, ExpiresAt: now.Add(time.Hour)}, Grant: microvm.ManagedGrant{ID: "reservation-first", Credits: 100, ExpiresAt: now.Add(30 * time.Second)}}, LastTick: now}
	policy, _ := json.Marshal(state)
	machine, _ := json.Marshal(microvm.Sandbox{ID: "sbx-managed", AppID: "managed", State: microvm.StateRunning})
	_, err = db.Exec(`INSERT INTO managed_apps VALUES('managed',?)`, policy)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO sandboxes VALUES('sbx-managed','running',?)`, machine)
	if err != nil {
		t.Fatal(err)
	}
	kills := []string{}
	w := Watchdog{DB: db, Heartbeat: filepath.Join(t.TempDir(), "heartbeat"), Now: func() time.Time { return now }, Kill: func(id string) error { kills = append(kills, id); return nil }}
	if err = w.Step(context.Background()); err != nil || len(kills) != 0 {
		t.Fatal("funded native process killed", err, kills)
	}
	if err = Check(w.Heartbeat); err != nil {
		t.Fatal(err)
	}
	now = now.Add(5 * time.Second)
	if err = w.Step(context.Background()); err != nil || len(kills) != 1 {
		t.Fatal("stalled runtime survived independent watchdog", err, kills)
	}
	// A recent daemon heartbeat cannot extend an expired credit lease.
	state.LastTick = now
	state.Grant.ExpiresAt = now.Add(4 * time.Second)
	policy, _ = json.Marshal(state)
	_, _ = db.Exec(`UPDATE managed_apps SET record=?`, policy)
	if err = w.Step(context.Background()); err != nil || len(kills) != 2 {
		t.Fatal("expired lease survived", err, kills)
	}
}
func TestWatchdogReadFailureDeniesAdmissionAndKillsManagedHost(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sbx-frozen"), 0700); err != nil {
		t.Fatal(err)
	}
	db, _ := sql.Open("sqlite", filepath.Join(t.TempDir(), "missing.db"))
	defer db.Close()
	kills := []string{}
	w := Watchdog{DB: db, Cgroups: root, Heartbeat: filepath.Join(t.TempDir(), "heartbeat"), Kill: func(id string) error { kills = append(kills, id); return nil }}
	if err := w.Step(context.Background()); err == nil {
		t.Fatal("accepted unreadable state")
	}
	if len(kills) != 1 || kills[0] != "sbx-frozen" {
		t.Fatal("did not kill unaccounted process", kills)
	}
	if Check(w.Heartbeat) == nil {
		t.Fatal("failed watchdog allowed runtime admission")
	}
}
