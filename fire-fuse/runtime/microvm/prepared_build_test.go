package microvm

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestPreparedBuildCompletesDirectlyFromReceiving(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			store := factory(t)
			now := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
			createTestUser(t, store, "usr_alice", now)
			app := App{ID: "app_prepared", Slug: "prepared", CreatedAt: now, UpdatedAt: now}
			runtime := testAppSessionRuntime()
			runtime.ActiveBuildID = ""
			if err := store.CreateProvisioningAppForComputeTenant("usr_alice", app, runtime); err != nil {
				t.Fatal(err)
			}
			operation := AppBuildOperation{ID: "bop_0123456789abcdef0123456789abcdef", AppID: app.ID,
				Status: AppBuildOperationReceiving, CreatedAt: now, UpdatedAt: now}
			if err := store.BeginAppBuildUpload("usr_alice", operation); err != nil {
				t.Fatal(err)
			}
			buildID := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			receipt, build, err := store.CompletePreparedAppBuild(operation.ID, buildID, now.Add(time.Second))
			if err != nil || receipt.Status != AppBuildOperationSucceeded || receipt.BuildVersion != 1 || build.BuildID != buildID {
				t.Fatalf("complete prepared Build = %#v, %#v, %v", receipt, build, err)
			}
			selected, err := store.GetAppSessionRuntime(app.ID)
			if err != nil || selected.ActiveBuildID != "" {
				t.Fatalf("prepared Build changed active selection = %#v, %v", selected, err)
			}
			if _, _, err := store.CompletePreparedAppBuild(operation.ID, buildID, now.Add(2*time.Second)); err == nil {
				t.Fatal("duplicate prepared completion was accepted instead of reading its receipt")
			}
		})
	}
}

func TestReplaySelectionProtectsOldActiveBuildFromRetention(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			store := factory(t)
			at := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
			createTestUser(t, store, "usr_alice", at)
			app := App{ID: "app_replay_retention", Slug: "replay", CreatedAt: at, UpdatedAt: at}
			runtime := testAppSessionRuntime()
			runtime.ActiveBuildID = ""
			if err := store.CreateProvisioningAppForComputeTenant("usr_alice", app, runtime); err != nil {
				t.Fatal(err)
			}
			oldID := fmt.Sprintf("sha256:%064x", 1)
			for version := 1; version <= 26; version++ {
				operation := AppBuildOperation{ID: fmt.Sprintf("bop_%032x", version), AppID: app.ID,
					Status: AppBuildOperationReceiving, CreatedAt: at.Add(time.Duration(version) * time.Second), UpdatedAt: at.Add(time.Duration(version) * time.Second)}
				if err := store.BeginAppBuildUpload("usr_alice", operation); err != nil {
					t.Fatalf("begin Build %d: %v", version, err)
				}
				if _, _, err := store.CompletePreparedAppBuild(operation.ID, fmt.Sprintf("sha256:%064x", version), at.Add(time.Duration(version)*time.Second)); err != nil {
					t.Fatalf("complete Build %d: %v", version, err)
				}
				if err := store.SelectAppBuildForComputeTenant("usr_alice", app.ID, oldID, at.Add(time.Duration(version)*time.Second)); err != nil {
					t.Fatalf("reselect active Build after %d: %v", version, err)
				}
			}
			selected, err := store.GetAppSessionRuntime(app.ID)
			if err != nil || selected.ActiveBuildID != oldID {
				t.Fatalf("retained active Build = %#v, %v", selected, err)
			}
		})
	}
}

func TestSelectRetainedBuildForFutureSessions(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			store := factory(t)
			at := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
			createTestUser(t, store, "usr_alice", at)
			createTestUser(t, store, "usr_bob", at)
			app := App{ID: "app_select_build", Slug: "selected", CreatedAt: at, UpdatedAt: at}
			runtime := testAppSessionRuntime()
			runtime.ActiveBuildID = ""
			if err := store.CreateProvisioningAppForComputeTenant("usr_alice", app, runtime); err != nil {
				t.Fatal(err)
			}
			oldID := "sha256:" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			newID := "sha256:" + "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
			for index, buildID := range []string{oldID, newID} {
				operation := AppBuildOperation{ID: []string{"bop_0123456789abcdef0123456789abcdef", "bop_1123456789abcdef0123456789abcdef"}[index], AppID: app.ID,
					Status: AppBuildOperationReceiving, CreatedAt: at.Add(time.Duration(index) * time.Second), UpdatedAt: at.Add(time.Duration(index) * time.Second)}
				if err := store.BeginAppBuildUpload("usr_alice", operation); err != nil {
					t.Fatal(err)
				}
				if _, _, err := store.CompletePreparedAppBuild(operation.ID, buildID, at.Add(time.Duration(index+1)*time.Second)); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.SelectAppBuildForComputeTenant("usr_bob", app.ID, oldID, at.Add(3*time.Second)); !errors.Is(err, ErrAppNotFound) {
				t.Fatalf("cross-tenant selection = %v", err)
			}
			if err := store.SelectAppBuildForComputeTenant("usr_alice", app.ID, "sha256:"+"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", at.Add(3*time.Second)); !errors.Is(err, ErrAppBuildNotFound) {
				t.Fatalf("unknown Build selection = %v", err)
			}
			if err := store.SelectAppBuildForComputeTenant("usr_alice", app.ID, oldID, at.Add(3*time.Second)); err != nil {
				t.Fatal(err)
			}
			if err := store.SelectAppBuildForComputeTenant("usr_alice", app.ID, oldID, at.Add(4*time.Second)); err != nil {
				t.Fatalf("idempotent selection: %v", err)
			}
			selected, err := store.GetAppSessionRuntime(app.ID)
			if err != nil || selected.ActiveBuildID != oldID {
				t.Fatalf("selected Build = %#v, %v", selected, err)
			}
			if err := store.SelectAppBuildForComputeTenant("usr_alice", app.ID, "", at.Add(5*time.Second)); err != nil {
				t.Fatal(err)
			}
			selected, err = store.GetAppSessionRuntime(app.ID)
			if err != nil || selected.ActiveBuildID != "" {
				t.Fatalf("cleared Build = %#v, %v", selected, err)
			}
		})
	}
}
