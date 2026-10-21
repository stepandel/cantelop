package workspacesync

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
)

func TestNewPublicationMovesOneInodeWithoutClobbering(t *testing.T) {
	directory := t.TempDir()
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := os.Mkdir(filepath.Join(directory, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"first", "second"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var results [2]error
	var wait sync.WaitGroup
	for i, name := range []string{"first", "second"} {
		wait.Add(1)
		go func(i int, name string) { defer wait.Done(); results[i] = publishNew(root, name, "nested/file") }(i, name)
	}
	wait.Wait()
	successes, conflicts := 0, 0
	for _, err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, os.ErrExist) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("lost no-overwrite precondition: %v", results)
	}
	data, err := os.ReadFile(filepath.Join(directory, "nested/file"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := root.Stat(string(data)); !os.IsNotExist(err) {
		t.Fatal("published source name still exists", err)
	}
	info, err := root.Stat("nested/file")
	if err != nil {
		t.Fatal(err)
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || stat.Nlink != 1 {
		t.Fatal("publication created an extra hard link")
	}
}

func TestNewPublicationRejectsParentSymlink(t *testing.T) {
	directory := t.TempDir()
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(directory, "alias")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "prepared"), []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := publishNew(root, "prepared", "alias/file"); err == nil {
		t.Fatal("followed a parent symlink")
	}
	if _, err := os.Stat(filepath.Join(outside, "file")); !os.IsNotExist(err) {
		t.Fatal("escaped the selected root", err)
	}
}
