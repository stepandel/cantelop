// Package managedwatchdog is independent of runtime's process and timers. It
// reads committed state and kills VM cgroups when their funding/heartbeat ends.
package managedwatchdog

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

var sandboxID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,63}$`)

type Watchdog struct {
	DB        *sql.DB
	Cgroups   string
	Heartbeat string
	Now       func() time.Time
	Kill      func(string) error
}

func (w *Watchdog) healthy(state microvm.ManagedState, now time.Time) bool {
	return state.Policy.Active && state.Grant.ID != "" && state.Consumed < state.Grant.Credits && now.Before(state.Policy.ExpiresAt) && now.Before(state.Grant.ExpiresAt.Add(-5*time.Second)) && !state.LastTick.IsZero() && now.Sub(state.LastTick) >= 0 && now.Sub(state.LastTick) <= 4*time.Second
}
func (w *Watchdog) kill(id string) error {
	if !sandboxID.MatchString(id) {
		return errors.New("invalid watchdog cgroup identity")
	}
	if w.Kill != nil {
		return w.Kill(id)
	}
	dir := filepath.Join(w.Cgroups, id)
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("unsafe watchdog cgroup")
	}
	if err = os.WriteFile(filepath.Join(dir, "cgroup.kill"), []byte("1"), 0600); os.IsNotExist(err) {
		return nil
	}
	return err
}
func (w *Watchdog) killAll() error {
	dirs, err := os.ReadDir(w.Cgroups)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var failures []error
	for _, dir := range dirs {
		if sandboxID.MatchString(dir.Name()) {
			failures = append(failures, w.kill(dir.Name()))
		}
	}
	return errors.Join(failures...)
}
func (w *Watchdog) Step(ctx context.Context) error {
	now := time.Now().UTC()
	if w.Now != nil {
		now = w.Now()
	}
	// All sandboxes on a managed host are managed. If SQLite is unreadable, no
	// possibly unfunded process may keep running while we guess its ownership.
	rows, err := w.DB.QueryContext(ctx, `SELECT m.record,s.record FROM managed_apps m JOIN sandboxes s ON CASE WHEN json_valid(s.record) THEN json_extract(s.record,'$.app_id') END=m.app_id WHERE s.state IN ('starting','running','stopping','failed')`)
	if err != nil {
		_ = w.status(false)
		return errors.Join(err, w.killAll())
	}
	var failures []error
	for rows.Next() {
		var policy, record []byte
		if err = rows.Scan(&policy, &record); err != nil {
			failures = append(failures, err)
			break
		}
		var state microvm.ManagedState
		var machine microvm.Sandbox
		if json.Unmarshal(policy, &state) != nil || json.Unmarshal(record, &machine) != nil {
			failures = append(failures, errors.New("invalid managed watchdog state"))
			break
		}
		if machine.State == microvm.StateFailed || !w.healthy(state, now) {
			failures = append(failures, w.kill(machine.ID))
		}
	}
	failures = append(failures, rows.Err(), rows.Close())
	err = errors.Join(failures...)
	if err != nil {
		_ = w.status(false)
		return errors.Join(err, w.killAll())
	}
	return w.status(true)
}
func (w *Watchdog) status(ready bool) error {
	if w.Heartbeat == "" {
		return nil
	}
	value := "denied"
	if ready {
		value = "ready"
	}
	temp := w.Heartbeat + ".tmp"
	if err := os.WriteFile(temp, []byte(value), 0600); err != nil {
		return err
	}
	return os.Rename(temp, w.Heartbeat)
}

// Check is called by runtime admission AND its periodic shutdown loop.
func Check(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	age := time.Since(info.ModTime())
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || age < 0 || age > 4*time.Second {
		return errors.New("managed watchdog unavailable")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "ready" {
		return errors.New("managed watchdog unavailable")
	}
	return nil
}

// HostID changes with an OS boot. An expired or copied grant cannot be replayed
// on another host by reusing its ingress URL or a restored database snapshot.
func HostID() (string, error) {
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", err
	}
	if len(boot) < 32 {
		return "", errors.New("host boot identity unavailable")
	}
	digest := sha256.Sum256(boot)
	return "host_" + hex.EncodeToString(digest[:16]), nil
}
