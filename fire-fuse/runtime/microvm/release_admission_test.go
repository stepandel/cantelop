package microvm

import (
	"errors"
	"testing"
	"time"
)

func TestReleaseAdmissionClosesNewSessionsWithoutBreakingReplay(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			store := factory(t)
			seedTestApp(t, store, "app-test", "wsp_project123")
			at := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
			first := releaseTestSession("ses_first", at)
			firstSandbox := releaseTestSandbox("sbx-first", at)
			if _, inserted, err := store.CreateSession(first, firstSandbox, ""); err != nil || !inserted {
				t.Fatalf("initial Session = %t, %v", inserted, err)
			}
			generation := "act_0123456789abcdef0123456789abcdef"
			if _, err := store.SetReleaseAdmissionForComputeTenant("usr_other", "app-test", generation, true); !errors.Is(err, ErrAppNotFound) {
				t.Fatalf("cross-tenant close = %v", err)
			}
			state, err := store.SetReleaseAdmissionForComputeTenant("usr_test", "app-test", generation, true)
			if err != nil || !state.Closed || state.Generation != generation {
				t.Fatalf("closed state = %#v, %v", state, err)
			}
			if got, err := store.GetReleaseAdmissionForComputeTenant("usr_test", "app-test"); err != nil || got != state {
				t.Fatalf("readback = %#v, %v", got, err)
			}
			if _, inserted, err := store.CreateSession(first, firstSandbox, ""); err != nil || inserted {
				t.Fatalf("existing Session replay while closed = %t, %v", inserted, err)
			}
			second := releaseTestSession("ses_second", at)
			if _, _, err := store.CreateSession(second, releaseTestSandbox("sbx-second", at), ""); !errors.Is(err, ErrReleaseAdmissionClosed) {
				t.Fatalf("new Session while closed = %v", err)
			}
			if _, err := store.SetReleaseAdmissionForComputeTenant("usr_test", "app-test", "act_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", false); !errors.Is(err, ErrReleaseAdmissionConflict) {
				t.Fatalf("wrong-generation open = %v", err)
			}
			state, err = store.SetReleaseAdmissionForComputeTenant("usr_test", "app-test", generation, false)
			if err != nil || state.Closed {
				t.Fatalf("open state = %#v, %v", state, err)
			}
			newer := "act_ffffffffffffffffffffffffffffffff"
			if _, err := store.SetReleaseAdmissionForComputeTenant("usr_test", "app-test", newer, true); err != nil {
				t.Fatalf("newer close = %v", err)
			}
			if _, err := store.SetReleaseAdmissionForComputeTenant("usr_test", "app-test", generation, true); !errors.Is(err, ErrReleaseAdmissionConflict) {
				t.Fatalf("stale close = %v", err)
			}
			if _, err := store.SetReleaseAdmissionForComputeTenant("usr_test", "app-test", newer, false); err != nil {
				t.Fatalf("newer open = %v", err)
			}
			if _, inserted, err := store.CreateSession(second, releaseTestSandbox("sbx-second", at), ""); err != nil || !inserted {
				t.Fatalf("new Session after open = %t, %v", inserted, err)
			}
		})
	}
}

func releaseTestSession(id string, at time.Time) Session {
	return Session{ID: id, WorkspaceID: "wsp_project123", BuildID: testImageID,
		KeepAliveSeconds: 300, CreatedAt: at, UpdatedAt: at}
}

func releaseTestSandbox(id string, at time.Time) Sandbox {
	return Sandbox{ID: id, AppID: "app-test", WorkspaceID: "wsp_project123",
		Profile: DefaultSandboxProfileName, BuildID: testImageID, VCPUs: DefaultSandboxVCPUs,
		MemoryMiB: DefaultSandboxMemoryMiB, State: StateStarting, CreatedAt: at, UpdatedAt: at}
}
