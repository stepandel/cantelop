package microvm

import (
	"errors"
	"testing"
	"time"
)

func TestSQLiteComputeTenantProjection(t *testing.T) {
	store := openTestSQLiteStore(t)
	tenant := ComputeTenant{ID: "usr_alice", CreatedAt: time.Now().UTC()}
	created, err := store.EnsureComputeTenant(tenant)
	if err != nil || !created {
		t.Fatalf("EnsureComputeTenant() = %v, %v; want created", created, err)
	}
	created, err = store.EnsureComputeTenant(ComputeTenant{ID: tenant.ID, CreatedAt: tenant.CreatedAt.Add(time.Hour)})
	if err != nil || created {
		t.Fatalf("idempotent EnsureComputeTenant() = %v, %v; want existing", created, err)
	}
	got, err := store.GetComputeTenant(tenant.ID)
	if err != nil || got != tenant {
		t.Fatalf("GetComputeTenant() = %#v, %v; want %#v", got, err, tenant)
	}
	if _, err := store.GetComputeTenant("usr_missing"); !errors.Is(err, ErrComputeTenantMissing) {
		t.Fatalf("GetComputeTenant(missing) error = %v", err)
	}
}
