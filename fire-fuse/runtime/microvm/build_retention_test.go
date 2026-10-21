package microvm

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestBuildRetentionAdmissionAndReplay(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			store := factory(t)
			now := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
			createTestUser(t, store, "usr_alice", now)
			app := App{ID: "app_retention", Slug: "retention", CreatedAt: now, UpdatedAt: now}
			runtime := testAppSessionRuntime()
			runtime.ActiveBuildID = ""
			if err := store.CreateProvisioningAppForComputeTenant("usr_alice", app, runtime); err != nil {
				t.Fatal(err)
			}
			for version := 1; version <= 31; version++ {
				at := now.Add(time.Duration(version) * time.Minute)
				op := AppBuildOperation{ID: fmt.Sprintf("bop_%032x", version), AppID: app.ID, Status: AppBuildOperationReceiving, CreatedAt: at, UpdatedAt: at}
				if version == 26 {
					if err := store.BeginAppBuildUpload("usr_foreign", op); !errors.Is(err, ErrAppNotFound) {
						t.Fatalf("foreign admission: %v", err)
					}
					builds, _ := store.ListAppBuildsForApp(app.ID)
					if len(builds) != 25 {
						t.Fatal("foreign admission pruned builds")
					}
				}
				if err := store.BeginAppBuildUpload("usr_alice", op); err != nil {
					t.Fatalf("admit version %d: %v", version, err)
				}
				if version == 26 {
					builds, _ := store.ListAppBuildsForApp(app.ID)
					if len(builds) != 20 || builds[0].Version != 6 || builds[19].Version != 25 {
						t.Fatalf("retained: %#v", builds)
					}
					retired, err := store.GetAppBuildOperationForComputeTenant("usr_alice", app.ID, fmt.Sprintf("bop_%032x", 1))
					if err != nil || retired.Status != AppBuildOperationFailed || retired.ErrorCode != "build_retired" {
						t.Fatalf("retired receipt: %#v %v", retired, err)
					}
					if err := store.BeginAppBuildUpload("usr_alice", op); !errors.Is(err, ErrAppBuildUploadPending) {
						t.Fatalf("duplicate: %v", err)
					}
				}
				_, build, err := store.CompletePreparedAppBuild(op.ID, fmt.Sprintf("sha256:%064x", version), at)
				if err != nil || build.Version != version {
					t.Fatalf("complete %d: %#v %v", version, build, err)
				}
			}
			// Once no upload is pending, an old retired command still cannot be reused.
			op := AppBuildOperation{ID: fmt.Sprintf("bop_%032x", 1), AppID: app.ID, Status: AppBuildOperationReceiving, CreatedAt: now, UpdatedAt: now}
			if err := store.BeginAppBuildUpload("usr_alice", op); !errors.Is(err, ErrAppBuildUploadPending) {
				t.Fatalf("retired replay: %v", err)
			}
		})
	}
}
