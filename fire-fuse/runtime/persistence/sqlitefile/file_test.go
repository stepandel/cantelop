package sqlitefile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareCreatesOwnerOnlyRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	if err := Prepare(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("state file mode = %v", info.Mode())
	}
}

func TestPrepareRejectsSymlink(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "state.db")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := Prepare(link); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("Prepare() = %v, want ErrUnsafe", err)
	}
}

func TestAcquireLockExcludesSecondOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db.lock")
	first, err := AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if second, err := AcquireLock(path); second != nil || !errors.Is(err, ErrLocked) {
		t.Fatalf("AcquireLock() = %#v, %v", second, err)
	}
}
