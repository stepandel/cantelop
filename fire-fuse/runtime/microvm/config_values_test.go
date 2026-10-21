package microvm

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/model"
)

// Per-App config quotas enforced by the SQLite config repository.
const (
	maxConfigEntriesPerScope = 128
	maxConfigBytesPerScope   = 512 * 1024
)

func TestConfigStoreScopesKindsAndUpdates(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, configEncryptionKeyBytes)
	factories := map[string]func(*testing.T) Store{
		"sqlite": func(t *testing.T) Store {
			store, err := OpenSQLiteStore(filepath.Join(t.TempDir(), "state.db"), WithConfigEncryptionKey(key))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			return store
		},
	}
	for name, factory := range factories {
		t.Run(name, func(t *testing.T) {
			store := factory(t)
			seedTestApp(t, store, "app-test", "wsp_project123")
			now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

			if err := store.SetAppConfig("app-test", ConfigValue{Name: "LOG_LEVEL", Kind: ConfigValueVariable, Value: "info", UpdatedAt: now}); err != nil {
				t.Fatal(err)
			}
			if err := store.SetAppConfig("app-test", ConfigValue{Name: "API_TOKEN", Kind: ConfigValueSecret, Value: "top-secret", UpdatedAt: now}); err != nil {
				t.Fatal(err)
			}
			if err := store.SetAppConfig("app-test", ConfigValue{Name: "LOG_LEVEL", Kind: ConfigValueVariable, Value: "debug", UpdatedAt: now.Add(time.Second)}); err != nil {
				t.Fatalf("same-kind update: %v", err)
			}
			if err := store.SetAppConfig("app-test", ConfigValue{Name: "LOG_LEVEL", Kind: ConfigValueSecret, Value: "debug", UpdatedAt: now.Add(2 * time.Second)}); !errors.Is(err, ErrConfigKindConflict) {
				t.Fatalf("cross-kind update error = %v, want ErrConfigKindConflict", err)
			}
			appValues, err := store.ListAppConfig("app-test")
			if err != nil || len(appValues) != 2 || appValues[0].Name != "API_TOKEN" || appValues[0].Value != "top-secret" || appValues[1].Value != "debug" {
				t.Fatalf("ListAppConfig() = %#v, %v", appValues, err)
			}
			resolved, err := store.ResolveAppConfig("app-test")
			if err != nil || len(resolved) != 2 || resolved[0] != "API_TOKEN=top-secret" || resolved[1] != "LOG_LEVEL=debug" {
				t.Fatalf("ResolveAppConfig() = %#v, %v", resolved, err)
			}
			deleted, err := store.DeleteAppConfig("app-test", "API_TOKEN")
			if err != nil || !deleted {
				t.Fatalf("DeleteAppConfig() = %t, %v", deleted, err)
			}
			deleted, err = store.DeleteAppConfig("app-test", "API_TOKEN")
			if err != nil || deleted {
				t.Fatalf("replayed DeleteAppConfig() = %t, %v", deleted, err)
			}
		})
	}
}

func TestConfigRejectsPlatformReservedNames(t *testing.T) {
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	value := ConfigValue{
		Name: "CANTELOP_INTERNAL_PORT", Kind: ConfigValueVariable,
		Value: "9999", UpdatedAt: now,
	}
	if err := value.Validate(); !errors.Is(err, ErrInvalidConfigValue) {
		t.Fatalf("reserved ConfigValue.Validate() error = %v, want ErrInvalidConfigValue", err)
	}
	if err := model.ValidateConfigName("CANTELOP_CUSTOM"); !errors.Is(err, ErrInvalidConfigValue) {
		t.Fatalf("model.ValidateConfigName() error = %v, want ErrInvalidConfigValue", err)
	}
}

func TestSQLiteConfigSecretsAreEncryptedAtRest(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "state.db")
	key := bytes.Repeat([]byte{0x37}, configEncryptionKeyBytes)
	store, err := OpenSQLiteStore(path, WithConfigEncryptionKey(key))
	if err != nil {
		t.Fatal(err)
	}
	seedTestApp(t, store, "app-test", "wsp_project123")
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	const secret = "plaintext-must-not-reach-sqlite"
	if err := store.SetAppConfig("app-test", ConfigValue{Name: "TOKEN", Kind: ConfigValueSecret, Value: secret, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	var valueText *string
	var nonce, ciphertext []byte
	if err := store.database.SQL().QueryRow("SELECT value_text, value_nonce, value_ciphertext FROM app_config_values WHERE app_id = ? AND name = ?", "app-test", "TOKEN").Scan(&valueText, &nonce, &ciphertext); err != nil {
		t.Fatal(err)
	}
	if valueText != nil || len(nonce) == 0 || len(ciphertext) == 0 || bytes.Contains(ciphertext, []byte(secret)) {
		t.Fatalf("secret row was not sealed: text=%v nonce=%d ciphertext=%d", valueText, len(nonce), len(ciphertext))
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = OpenSQLiteStore(path, WithConfigEncryptionKey(key))
	if err != nil {
		t.Fatal(err)
	}
	values, err := store.ListAppConfig("app-test")
	if err != nil || len(values) != 1 || values[0].Value != secret {
		t.Fatalf("reopened ListAppConfig() = %#v, %v", values, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	wrongKey := bytes.Repeat([]byte{0x99}, configEncryptionKeyBytes)
	store, err = OpenSQLiteStore(path, WithConfigEncryptionKey(wrongKey))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.ListAppConfig("app-test"); !errors.Is(err, ErrConfigDecryption) {
		t.Fatalf("wrong-key ListAppConfig() error = %v, want ErrConfigDecryption", err)
	}
}

func TestConfigStoreReplacesRevisionedProjection(t *testing.T) {
	key := bytes.Repeat([]byte{0x52}, configEncryptionKeyBytes)
	factories := map[string]func(*testing.T) Store{
		"sqlite": func(t *testing.T) Store {
			store, err := OpenSQLiteStore(filepath.Join(t.TempDir(), "state.db"), WithConfigEncryptionKey(key))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			return store
		},
	}
	for name, factory := range factories {
		t.Run(name, func(t *testing.T) {
			store := factory(t)
			seedTestApp(t, store, "app-test", "wsp_project123")
			now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
			first := []ConfigValue{
				{Name: "LOG_LEVEL", Kind: ConfigValueVariable, Value: "debug", UpdatedAt: now},
				{Name: "TOKEN", Kind: ConfigValueSecret, Value: "first-secret", UpdatedAt: now},
			}
			applied, err := store.ReplaceAppConfig("app-test", 2, first)
			if err != nil || !applied {
				t.Fatalf("ReplaceAppConfig(first) = %t, %v", applied, err)
			}
			applied, err = store.ReplaceAppConfig("app-test", 2, nil)
			if err != nil || applied {
				t.Fatalf("ReplaceAppConfig(replay) = %t, %v", applied, err)
			}
			if _, err := store.ReplaceAppConfig("app-test", 1, nil); !errors.Is(err, ErrStaleConfigProjection) {
				t.Fatalf("ReplaceAppConfig(stale) error = %v, want ErrStaleConfigProjection", err)
			}
			second := []ConfigValue{{Name: "TOKEN", Kind: ConfigValueSecret, Value: "second-secret", UpdatedAt: now.Add(time.Second)}}
			applied, err = store.ReplaceAppConfig("app-test", 3, second)
			if err != nil || !applied {
				t.Fatalf("ReplaceAppConfig(second) = %t, %v", applied, err)
			}
			values, err := store.ListAppConfig("app-test")
			if err != nil || len(values) != 1 || values[0].Name != "TOKEN" || values[0].Value != "second-secret" {
				t.Fatalf("ListAppConfig() = %#v, %v", values, err)
			}
		})
	}
}

func TestSQLiteConfigSecretRequiresKey(t *testing.T) {
	store, err := OpenSQLiteStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	seedTestApp(t, store, "app-test", "wsp_project123")
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	if err := store.SetAppConfig("app-test", ConfigValue{Name: "TOKEN", Kind: ConfigValueSecret, Value: "secret", UpdatedAt: now}); !errors.Is(err, ErrConfigEncryptionRequired) {
		t.Fatalf("SetAppConfig() error = %v, want ErrConfigEncryptionRequired", err)
	}
	if err := store.SetAppConfig("app-test", ConfigValue{Name: "PORT", Kind: ConfigValueVariable, Value: "3000", UpdatedAt: now}); err != nil {
		t.Fatalf("plaintext variable should not require encryption key: %v", err)
	}
}

func TestConfigValueValidation(t *testing.T) {
	now := time.Now().UTC()
	for _, value := range []ConfigValue{
		{Name: "1BAD", Kind: ConfigValueVariable, Value: "x", UpdatedAt: now},
		{Name: "GOOD", Kind: "password", Value: "x", UpdatedAt: now},
		{Name: "GOOD", Kind: ConfigValueVariable, Value: "bad\x00value", UpdatedAt: now},
		{Name: "GOOD", Kind: ConfigValueVariable, Value: "x"},
	} {
		if !errors.Is(value.Validate(), ErrInvalidConfigValue) {
			t.Errorf("ConfigValue %#v passed validation", value)
		}
	}
}

func TestConfigStoreEnforcesScopeCardinality(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, configEncryptionKeyBytes)
	factories := map[string]func(*testing.T) Store{
		"sqlite": func(t *testing.T) Store {
			store, err := OpenSQLiteStore(filepath.Join(t.TempDir(), "state.db"), WithConfigEncryptionKey(key))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			return store
		},
	}
	for name, factory := range factories {
		t.Run(name, func(t *testing.T) {
			store := factory(t)
			seedTestApp(t, store, "app-test", "wsp_project123")
			now := time.Now().UTC()
			for index := 0; index < maxConfigEntriesPerScope; index++ {
				value := ConfigValue{Name: fmt.Sprintf("KEY_%03d", index), Kind: ConfigValueVariable, Value: "x", UpdatedAt: now}
				if err := store.SetAppConfig("app-test", value); err != nil {
					t.Fatalf("SetAppConfig(%d) error = %v", index, err)
				}
			}
			overflow := ConfigValue{Name: "OVERFLOW", Kind: ConfigValueVariable, Value: "x", UpdatedAt: now}
			if err := store.SetAppConfig("app-test", overflow); !errors.Is(err, ErrConfigQuotaExceeded) {
				t.Fatalf("overflow error = %v, want ErrConfigQuotaExceeded", err)
			}
			update := ConfigValue{Name: "KEY_000", Kind: ConfigValueVariable, Value: "updated", UpdatedAt: now}
			if err := store.SetAppConfig("app-test", update); err != nil {
				t.Fatalf("update at limit error = %v", err)
			}
		})
	}
}

func TestConfigStoreBoundsRuntimeAssignment(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, configEncryptionKeyBytes)
	factories := map[string]func(*testing.T) Store{
		"sqlite": func(t *testing.T) Store {
			store, err := OpenSQLiteStore(filepath.Join(t.TempDir(), "state.db"), WithConfigEncryptionKey(key))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			return store
		},
	}
	for name, factory := range factories {
		t.Run(name, func(t *testing.T) {
			store := factory(t)
			seedTestApp(t, store, "app-test", "wsp_project123")
			now := time.Now().UTC()
			const entries = 8
			valueSize := maxConfigBytesPerScope/entries - len("KEY_000")
			for index := 0; index < entries; index++ {
				value := ConfigValue{Name: fmt.Sprintf("KEY_%03d", index), Kind: ConfigValueVariable, Value: strings.Repeat("x", valueSize), UpdatedAt: now}
				if err := store.SetAppConfig("app-test", value); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := store.ResolveAppConfig("app-test"); !errors.Is(err, ErrConfigQuotaExceeded) {
				t.Fatalf("ResolveAppConfig() error = %v, want ErrConfigQuotaExceeded", err)
			}
		})
	}
}

func TestSQLiteConfigQuotaCountsSecretPlaintext(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, configEncryptionKeyBytes)
	store, err := OpenSQLiteStore(filepath.Join(t.TempDir(), "state.db"), WithConfigEncryptionKey(key))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	seedTestApp(t, store, "app-test", "wsp_project123")

	now := time.Now().UTC()
	const entries = 8
	valueSize := maxConfigBytesPerScope/entries - len("KEY_000")
	for index := 0; index < entries; index++ {
		value := ConfigValue{
			Name: fmt.Sprintf("KEY_%03d", index), Kind: ConfigValueSecret,
			Value: strings.Repeat("x", valueSize), UpdatedAt: now,
		}
		if err := store.SetAppConfig("app-test", value); err != nil {
			t.Fatalf("SetAppConfig(%d) error = %v", index, err)
		}
	}
}
