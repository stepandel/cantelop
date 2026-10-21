package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestConfigureObservabilityIsDisabledWithoutCredential(t *testing.T) {
	spool, relay, err := configureObservability("", "/unused", 64<<20, time.Hour)
	if err != nil || spool != nil || relay != nil {
		t.Fatalf("configureObservability() = %#v, %#v, %v", spool, relay, err)
	}
}

func TestConfigureObservabilityUsesPrivateCredentialAndSpool(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(directory, "ingest.token")
	if err := os.WriteFile(tokenFile, []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	spoolDirectory := filepath.Join(directory, "spool")
	spool, relay, err := configureObservability(tokenFile, spoolDirectory, 2<<20, time.Hour)
	if err != nil || spool == nil || relay == nil {
		t.Fatalf("configureObservability() = %#v, %#v, %v", spool, relay, err)
	}
}
