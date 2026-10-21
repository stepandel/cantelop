package auth

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stepandel/cantelop/clients/cli/internal/controlapi"
)

func TestCredentialsAreAtomicPrivateAndOriginPinned(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cantelop", "config.json")
	want := controlapi.Credentials{
		ConsoleURL: "https://console.example.com", ControlURL: "https://control.example.com", RefreshToken: "signed.cantelop.token",
	}
	if err := SaveCredentials(path, want); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("credential mode = %v, %v", info.Mode().Perm(), err)
	}
	got, err := LoadCredentials(path)
	if err != nil || got != want {
		t.Fatalf("loadCredentials() = %#v, %v", got, err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCredentials(path); err == nil {
		t.Fatal("loadCredentials() accepted a public credential file")
	}
}
