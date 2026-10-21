package microvm

import (
	"path/filepath"
	"testing"
	"time"
)

func TestSessionWorkLeaseStoresPreserveAndExpireLeases(t *testing.T) {
	for _, test := range []struct {
		name string
		new  func(*testing.T) Store
	}{
		{name: "sqlite", new: func(t *testing.T) Store {
			store, err := OpenSQLiteStore(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			return store
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := test.new(t)
			seedTestApp(t, store, "app-test", "wsp_project123")
			at := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
			sessionRecord := Session{
				ID: "ses_lease", WorkspaceID: "wsp_project123", BuildID: testImageID,
				KeepAliveSeconds: 300, CreatedAt: at, UpdatedAt: at,
			}
			sandbox := Sandbox{
				ID: deterministicInternalID("sbx-", sessionRecord.ID), AppID: "app-test",
				WorkspaceID: sessionRecord.WorkspaceID, Profile: DefaultSandboxProfileName,
				BuildID: testImageID, VCPUs: DefaultSandboxVCPUs, MemoryMiB: DefaultSandboxMemoryMiB,
				State: StateStarting, CreatedAt: at, UpdatedAt: at,
			}
			if _, _, err := store.CreateSession(sessionRecord, sandbox, ""); err != nil {
				t.Fatal(err)
			}
			lease := SessionWorkLease{
				SessionID: sessionRecord.ID, LeaseID: "lease_0123456789abcdef0123456789abcdef",
				ExpiresAt: at.Add(time.Minute), CreatedAt: at, UpdatedAt: at,
			}
			stored, created, err := store.AcquireSessionWorkLease(lease)
			if err != nil || !created || stored != lease {
				t.Fatalf("AcquireSessionWorkLease() = %#v, %t, %v", stored, created, err)
			}
			if replayed, created, err := store.AcquireSessionWorkLease(lease); err != nil || created || replayed != lease {
				t.Fatalf("replayed acquire = %#v, %t, %v", replayed, created, err)
			}
			renewed, err := store.RenewSessionWorkLease(lease.SessionID, lease.LeaseID, at.Add(2*time.Minute), at.Add(time.Second))
			if err != nil || !renewed.ExpiresAt.Equal(at.Add(2*time.Minute)) {
				t.Fatalf("RenewSessionWorkLease() = %#v, %v", renewed, err)
			}
			if changed, err := store.DeleteExpiredSessionWorkLeases(at.Add(time.Minute)); err != nil || len(changed) != 0 {
				t.Fatalf("early expiry = %#v, %v", changed, err)
			}
			if changed, err := store.DeleteExpiredSessionWorkLeases(at.Add(3 * time.Minute)); err != nil || len(changed) != 1 || changed[0] != lease.SessionID {
				t.Fatalf("expiry = %#v, %v", changed, err)
			}
			if values, err := store.ListSessionWorkLeases(lease.SessionID); err != nil || len(values) != 0 {
				t.Fatalf("leases after expiry = %#v, %v", values, err)
			}
		})
	}
}
