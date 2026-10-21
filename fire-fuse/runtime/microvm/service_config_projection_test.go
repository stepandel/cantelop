package microvm

import (
	"bytes"
	"errors"
	"testing"
)

func TestComputeConfigProjectionReplacesCacheByRevision(t *testing.T) {
	store := openTestSQLiteStore(t, WithConfigEncryptionKey(bytes.Repeat([]byte{0x42}, configEncryptionKeyBytes)))
	seedTestApp(t, store, "app-config-projection", "wsp_config_projection")
	service := NewService(store)
	values := []ConfigValue{
		{Name: "LOG_LEVEL", Kind: ConfigValueVariable, Value: "debug"},
		{Name: "TOKEN", Kind: ConfigValueSecret, Value: "secret"},
	}
	applied, err := service.ReplaceComputeAppConfigForUser("usr_test", "app-config-projection", 2, values)
	if err != nil || !applied {
		t.Fatalf("ReplaceComputeAppConfigForUser() = %t, %v", applied, err)
	}
	assignments, err := store.ResolveAppConfig("app-config-projection")
	if err != nil || len(assignments) != 2 || assignments[0] != "LOG_LEVEL=debug" || assignments[1] != "TOKEN=secret" {
		t.Fatalf("ResolveAppConfig() = %#v, %v", assignments, err)
	}
	applied, err = service.ReplaceComputeAppConfigForUser("usr_test", "app-config-projection", 2, values)
	if err != nil || applied {
		t.Fatalf("replayed ReplaceComputeAppConfigForUser() = %t, %v", applied, err)
	}
	if _, err := service.ReplaceComputeAppConfigForUser("usr_test", "app-config-projection", 1, nil); !errors.Is(err, ErrStaleConfigProjection) {
		t.Fatalf("stale ReplaceComputeAppConfigForUser() error = %v, want ErrStaleConfigProjection", err)
	}
}
