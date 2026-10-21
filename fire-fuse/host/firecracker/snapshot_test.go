package firecracker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPrepareSnapshotOutputCreatesOwnedDirectory(t *testing.T) {
	layout, err := NewJailLayout(shortSnapshotTempDir(t), "/usr/bin/firecracker", "snapshot-template")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(layout.RootDir, 0o750); err != nil {
		t.Fatal(err)
	}
	var ownedPath string
	if err := layout.prepareSnapshotOutput(123, 456, func(path string, uid, gid int) error {
		ownedPath = path
		if uid != 123 || gid != 456 {
			t.Fatalf("ownership = %d:%d", uid, gid)
		}
		return nil
	}); err != nil {
		t.Fatalf("prepareSnapshotOutput() error = %v", err)
	}
	if ownedPath != layout.HostSnapshotDirectory {
		t.Fatalf("owned path = %q", ownedPath)
	}
	info, err := os.Stat(layout.HostSnapshotDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("snapshot directory mode = %v", info.Mode())
	}
}

func TestPrepareSnapshotOutputRefusesExistingDirectory(t *testing.T) {
	layout, err := NewJailLayout(shortSnapshotTempDir(t), "/usr/bin/firecracker", "snapshot-template")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(layout.HostSnapshotDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	err = layout.prepareSnapshotOutput(123, 456, func(string, int, int) error { return nil })
	if !errors.Is(err, ErrJailAlreadyExists) {
		t.Fatalf("PrepareSnapshotOutput() error = %v, want ErrJailAlreadyExists", err)
	}
	if _, err := os.Stat(filepath.Dir(layout.HostSnapshotDirectory)); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareRestoreInputsBindsStateAndMemory(t *testing.T) {
	layout, kernel, platform, container := testAssetLayout(t)
	prepareTestJail(t, layout, kernel, platform, container)
	state := writeSnapshotInput(t, "vmstate", "state")
	memory := writeSnapshotInput(t, "memory", "memory")
	var binds [][2]string
	var chowned []string
	err := layout.prepareRestoreInputs(
		context.Background(),
		state,
		memory,
		1000,
		1001,
		func(path string, uid, gid int) error {
			if uid != 1000 || gid != 1001 {
				t.Errorf("ownership = %d:%d", uid, gid)
			}
			chowned = append(chowned, path)
			return nil
		},
		func(source, destination string) error {
			binds = append(binds, [2]string{source, destination})
			return copyFile(source, destination, 0o400)
		},
		func(string) error { return nil },
	)
	if err != nil {
		t.Fatalf("prepareRestoreInputs() error = %v", err)
	}
	if want := [][2]string{
		{state, layout.HostSnapshotState},
		{memory, layout.HostSnapshotMemory},
	}; !reflect.DeepEqual(binds, want) {
		t.Fatalf("binds = %#v, want %#v", binds, want)
	}
	if want := []string{layout.HostSnapshotDirectory}; !reflect.DeepEqual(chowned, want) {
		t.Fatalf("chowned = %q, want %q", chowned, want)
	}
}

func TestPrepareRestoreInputsRollsBackPartialBind(t *testing.T) {
	layout, kernel, platform, container := testAssetLayout(t)
	prepareTestJail(t, layout, kernel, platform, container)
	state := writeSnapshotInput(t, "vmstate", "state")
	memory := writeSnapshotInput(t, "memory", "memory")
	bindErr := errors.New("memory bind failed")
	var unmounted []string
	err := layout.prepareRestoreInputs(
		context.Background(),
		state,
		memory,
		1000,
		1001,
		func(string, int, int) error { return nil },
		func(source, destination string) error {
			if source == memory {
				return bindErr
			}
			return copyFile(source, destination, 0o400)
		},
		func(path string) error {
			unmounted = append(unmounted, path)
			return nil
		},
	)
	if !errors.Is(err, bindErr) {
		t.Fatalf("prepareRestoreInputs() error = %v", err)
	}
	if want := []string{layout.HostSnapshotState}; !reflect.DeepEqual(unmounted, want) {
		t.Fatalf("unmounted = %q, want %q", unmounted, want)
	}
	if _, err := os.Stat(layout.HostSnapshotDirectory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("snapshot directory remains: %v", err)
	}
}

func prepareTestJail(
	t *testing.T,
	layout JailLayout,
	kernel,
	platform,
	container string,
) {
	t.Helper()
	if err := layout.prepareAssetsWithBinds(
		context.Background(),
		kernel,
		platform,
		container,
		1000,
		1001,
		func(string, int, int) error { return nil },
		func(source, destination string) error {
			return copyFile(source, destination, 0o400)
		},
		func(string) error { return nil },
	); err != nil {
		t.Fatal(err)
	}
}

func writeSnapshotInput(t *testing.T, name, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(contents), 0o444); err != nil {
		t.Fatal(err)
	}
	return path
}

func shortSnapshotTempDir(t *testing.T) string {
	t.Helper()
	path, err := os.MkdirTemp("/tmp", "ff-snapshot-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(path) })
	return path
}
