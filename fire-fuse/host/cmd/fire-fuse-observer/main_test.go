package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateListenAddress(t *testing.T) {
	for _, address := range []string{"127.0.0.1:8082", "[::1]:8082"} {
		if err := validateListenAddress(address); err != nil {
			t.Errorf("validateListenAddress(%q) error = %v", address, err)
		}
	}
	for _, address := range []string{"0.0.0.0:8082", "localhost:8082", "127.0.0.1:0"} {
		if err := validateListenAddress(address); err == nil {
			t.Errorf("validateListenAddress(%q) error = nil", address)
		}
	}
}

func TestLoadToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "observer.token")
	want := strings.Repeat("a", 32)
	if err := os.WriteFile(path, []byte(want+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadToken(path)
	if err != nil || got != want {
		t.Fatalf("loadToken() = %q, %v", got, err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if got, err := loadToken(path); err != nil || got != want {
		t.Fatalf("loadToken() rejected group-readable token: %q, %v", got, err)
	}
	for _, mode := range []os.FileMode{0o660, 0o644} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		if _, err := loadToken(path); err == nil {
			t.Fatalf("loadToken() accepted unsafe permissions %04o", mode)
		}
	}
}
