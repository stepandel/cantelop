package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/stepandel/cantelop/fire-fuse/config"
	"github.com/stepandel/cantelop/fire-fuse/runtime/imagecatalog"
)

const commandTestImageID = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestRunPublishesManifest(t *testing.T) {
	var stdout, stderr bytes.Buffer
	publishCalled := false
	exitCode := run([]string{
		"-config", "/config.json",
		"-manifest", "/manifest.json",
	}, &stdout, &stderr, dependencies{
		loadConfig: func(path string) (config.Config, error) {
			return config.Config{ImageCatalog: "/catalog.json", StateDB: "/state.db"}, nil
		},
		publish: func(catalogPath, manifestPath string) (imagecatalog.Result, error) {
			publishCalled = true
			if catalogPath != "/catalog.json" || manifestPath != "/manifest.json" {
				t.Fatalf("publish arguments = %q, %q", catalogPath, manifestPath)
			}
			return imagecatalog.Result{BuildID: commandTestImageID, Changed: true}, nil
		},
	})
	if exitCode != 0 || !publishCalled || stderr.Len() != 0 {
		t.Fatalf("run() code/called/stderr = %d/%v/%q", exitCode, publishCalled, stderr.String())
	}
	if got := stdout.String(); got != `{"build_id":"`+commandTestImageID+`","changed":true}`+"\n" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestRunRetiresUsingTrustedConfiguration(t *testing.T) {
	var stdout, stderr bytes.Buffer
	loaded := false
	retired := false
	exitCode := run([]string{
		"-config", "/config.json",
		"-retire", commandTestImageID,
	}, &stdout, &stderr, dependencies{
		loadConfig: func(path string) (config.Config, error) {
			loaded = true
			if path != "/config.json" {
				t.Fatalf("config path = %q", path)
			}
			return config.Config{ImageCatalog: "/catalog.json", StateDB: "/state.db"}, nil
		},
		retire: func(catalogPath, statePath, buildID string) (imagecatalog.RetirementResult, error) {
			retired = true
			if catalogPath != "/catalog.json" || statePath != "/state.db" || buildID != commandTestImageID {
				t.Fatalf("retire arguments = %q, %q, %q", catalogPath, statePath, buildID)
			}
			return imagecatalog.RetirementResult{
				BuildID:     buildID,
				RootFSImage: "/images/retired.ext4",
				Changed:     true,
			}, nil
		},
	})
	if exitCode != 0 || !loaded || !retired || stderr.Len() != 0 {
		t.Fatalf("run() code/loaded/retired/stderr = %d/%v/%v/%q", exitCode, loaded, retired, stderr.String())
	}
	want := `{"build_id":"` + commandTestImageID + `","rootfs_image":"/images/retired.ext4","changed":true}` + "\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
}

func TestRunCollectsRootFSUsingTrustedConfiguration(t *testing.T) {
	var stdout, stderr bytes.Buffer
	rootfsPath := "/var/lib/fire-fuse/images/sha256-" + strings.Repeat("b", 64) + ".ext4"
	collected := false
	exitCode := run([]string{
		"-config", "/config.json",
		"-gc-rootfs", rootfsPath,
	}, &stdout, &stderr, dependencies{
		loadConfig: func(path string) (config.Config, error) {
			if path != "/config.json" {
				t.Fatalf("config path = %q", path)
			}
			return config.Config{ImageCatalog: "/catalog.json", StateDB: "/state.db"}, nil
		},
		collect: func(catalogPath, statePath, gotRootFS string) (imagecatalog.CollectionResult, error) {
			collected = true
			if catalogPath != "/catalog.json" || statePath != "/state.db" || gotRootFS != rootfsPath {
				t.Fatalf("collect arguments = %q, %q, %q", catalogPath, statePath, gotRootFS)
			}
			return imagecatalog.CollectionResult{RootFSImage: rootfsPath, Changed: true}, nil
		},
	})
	if exitCode != 0 || !collected || stderr.Len() != 0 {
		t.Fatalf("run() code/collected/stderr = %d/%v/%q", exitCode, collected, stderr.String())
	}
	want := `{"rootfs_image":"` + rootfsPath + `","changed":true}` + "\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
}

func TestRunRequiresExactlyOneOperation(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"-manifest", "/manifest.json", "-retire", commandTestImageID},
		{"-retire", commandTestImageID, "-gc-rootfs", "/rootfs.ext4"},
		{"unexpected"},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr, dependencies{}); code != 2 {
			t.Fatalf("run(%q) code = %d, want 2", args, code)
		}
		if !strings.Contains(stderr.String(), "usage:") {
			t.Fatalf("run(%q) stderr = %q, want usage", args, stderr.String())
		}
	}
}

func TestRunReportsRetirementFailure(t *testing.T) {
	var stdout, stderr bytes.Buffer
	wantErr := errors.New("service owns lifecycle state")
	exitCode := run([]string{"-retire", commandTestImageID}, &stdout, &stderr, dependencies{
		loadConfig: func(string) (config.Config, error) {
			return config.Config{ImageCatalog: "/catalog", StateDB: "/state"}, nil
		},
		retire: func(string, string, string) (imagecatalog.RetirementResult, error) {
			return imagecatalog.RetirementResult{}, wantErr
		},
	})
	if exitCode != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), wantErr.Error()) {
		t.Fatalf("run() code/stdout/stderr = %d/%q/%q", exitCode, stdout.String(), stderr.String())
	}
}
