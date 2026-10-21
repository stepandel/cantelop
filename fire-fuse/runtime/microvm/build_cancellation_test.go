package microvm

import (
	"fmt"
	"testing"
	"time"
)

func TestBuildCancellationFencesReplayAndCompletion(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		for _, phase := range []string{"absent", "receiving"} {
			t.Run(name+"/"+phase, func(t *testing.T) {
				store := factory(t)
				at := time.Now().UTC()
				createTestUser(t, store, "usr_alice", at)
				app := App{ID: "app_cancel", Slug: "cancel", CreatedAt: at, UpdatedAt: at}
				runtime := testAppSessionRuntime()
				runtime.ActiveBuildID = ""
				if err := store.CreateProvisioningAppForComputeTenant("usr_alice", app, runtime); err != nil {
					t.Fatal(err)
				}
				id := fmt.Sprintf("bop_%032x", 1)
				op := AppBuildOperation{ID: id, AppID: app.ID, Status: AppBuildOperationReceiving, CreatedAt: at, UpdatedAt: at}
				if phase != "absent" {
					if err := store.BeginAppBuildUpload("usr_alice", op); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := store.CancelAppBuildUpload("usr_other", app.ID, id, at); err == nil {
					t.Fatal("foreign cancellation accepted")
				}
				for i := 0; i < 2; i++ {
					cancelled, err := store.CancelAppBuildUpload("usr_alice", app.ID, id, at)
					if err != nil || cancelled.ErrorCode != "deployment_cancelled" {
						t.Fatalf("cancel: %#v %v", cancelled, err)
					}
				}
				if err := store.BeginAppBuildUpload("usr_alice", op); err == nil {
					t.Fatal("replay accepted")
				}
				if _, _, err := store.CompletePreparedAppBuild(id, fmt.Sprintf("sha256:%064x", 1), at); err == nil {
					t.Fatal("late completion accepted")
				}
				op.ID = fmt.Sprintf("bop_%032x", 2)
				if err := store.BeginAppBuildUpload("usr_alice", op); err != nil {
					t.Fatalf("replacement blocked: %v", err)
				}
			})
		}
	}
}
