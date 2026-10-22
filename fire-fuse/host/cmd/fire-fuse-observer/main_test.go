package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestManagedControlClientRequiresPairedConfigurationAndUsesItsOwnCredential(t *testing.T) {
	path := filepath.Join(t.TempDir(), "managed.token")
	token := strings.Repeat("a", 64)
	if err := os.WriteFile(path, []byte(token+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "127.0.0.1:8180" || r.Header.Get("Authorization") != "Bearer "+token {
			t.Fatal("wrong managed origin or credential")
		}
		return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader("not found")), Header: make(http.Header)}, nil
	})
	client, err := managedControlClient("http://127.0.0.1:8180", path, time.Second, transport)
	if err != nil {
		t.Fatal(err)
	}
	_, status, _ := client.DatabaseAccess(context.Background(), "sbx-0123456789abcdef0123456789abcdef")
	if status != 404 {
		t.Fatal(status)
	}
	for _, config := range [][2]string{{"", path}, {"http://public.example.com", path}, {"http://127.0.0.1:8180", ""}} {
		if _, err := managedControlClient(config[0], config[1], time.Second, transport); err == nil {
			t.Fatal("invalid managed configuration accepted")
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
