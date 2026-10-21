package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/config"
	"github.com/stepandel/cantelop/fire-fuse/host/snapshotbuilder"
	"github.com/stepandel/cantelop/fire-fuse/host/snapshotbundle"
)

func TestRunBuildsDefaultProfileShape(t *testing.T) {
	var got snapshotbuilder.Config
	var output bytes.Buffer
	deps := testDependencies(&output)
	deps.build = func(
		_ context.Context,
		buildConfig snapshotbuilder.Config,
	) (snapshotbundle.Manifest, error) {
		got = buildConfig
		return snapshotbundle.Manifest{
			SchemaVersion:  snapshotbundle.SchemaVersion,
			CPUFingerprint: "test-fingerprint",
		}, nil
	}

	err := run(context.Background(), []string{
		"-config", "/etc/fire-fuse/test.json",
		"-placeholder", "/var/lib/fire-fuse/placeholder.ext4",
		"-output", "/var/lib/fire-fuse/snapshots/template-v1",
	}, deps)
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if got.KernelImage != "/kernel" ||
		got.PlatformRootFS != "/platform.ext4" ||
		got.Placeholder != "/var/lib/fire-fuse/placeholder.ext4" ||
		got.Destination != "/var/lib/fire-fuse/snapshots/template-v1" ||
		got.VCPUCount != 4 ||
		got.MemoryMiB != 512 || got.HotplugMemoryMiB != 4096 {
		t.Fatalf("build config = %#v", got)
	}
	var manifest snapshotbundle.Manifest
	if err := json.Unmarshal(output.Bytes(), &manifest); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if manifest.CPUFingerprint != "test-fingerprint" {
		t.Fatalf("CPU fingerprint = %q", manifest.CPUFingerprint)
	}
}

func TestRunStopsBeforeBuildOnUntrustedPlaceholder(t *testing.T) {
	var output bytes.Buffer
	deps := testDependencies(&output)
	deps.checkFile = func(string, string) error {
		return errors.New("untrusted")
	}
	buildCalled := false
	deps.build = func(
		context.Context,
		snapshotbuilder.Config,
	) (snapshotbundle.Manifest, error) {
		buildCalled = true
		return snapshotbundle.Manifest{}, nil
	}

	err := run(context.Background(), []string{
		"-placeholder", "/tmp/placeholder.ext4",
		"-output", "/var/lib/fire-fuse/snapshots/template-v1",
	}, deps)
	if err == nil || !strings.Contains(err.Error(), "validate placeholder") {
		t.Fatalf("run() error = %v", err)
	}
	if buildCalled {
		t.Fatal("build was called")
	}
}

func TestParseOptionsRequiresExplicitArtifacts(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{
			name: "placeholder",
			args: nil,
			want: "-placeholder is required",
		},
		{
			name: "output",
			args: []string{
				"-placeholder", "/placeholder",
			},
			want: "-output is required",
		},
		{
			name: "paths",
			args: []string{
				"-placeholder", "placeholder",
				"-output", "/bundle",
			},
			want: "-placeholder must be a clean absolute path",
		},
		{
			name: "legacy profile selector",
			args: []string{"-profile", "default", "-placeholder", "/placeholder", "-output", "/bundle"},
			want: "flag provided but not defined: -profile",
		},
		{
			name: "legacy size selector",
			args: []string{"-memory-mib", "256", "-placeholder", "/placeholder", "-output", "/bundle"},
			want: "flag provided but not defined: -memory-mib",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseOptions(test.args); err == nil ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf("parseOptions() error = %v, want %q", err, test.want)
			}
		})
	}
}

func testDependencies(output *bytes.Buffer) dependencies {
	cfg := config.Config{
		FirecrackerBin: "/firecracker",
		JailerBin:      "/jailer",
		JailerUID:      1000,
		JailerGID:      1001,
		RuntimeDir:     "/var/lib/fire-fuse",
		Network: config.NetworkConfig{
			IPBin: "/ip",
		},
		Timeouts: config.TimeoutsConfig{
			Startup:       config.Duration(30 * time.Second),
			APISocket:     config.Duration(time.Second),
			ShutdownGrace: config.Duration(time.Second),
			ShutdownForce: config.Duration(time.Second),
			Cleanup:       config.Duration(time.Second),
		},
		Profiles: map[string]config.Profile{
			"default": {
				KernelImage:    "/kernel",
				RootFSImage:    "/platform.ext4",
				SnapshotBundle: "/snapshots/ubuntu",
				BootArgs:       "console=ttyS0",
			},
		},
	}
	return dependencies{
		loadConfig: func(string) (config.Config, error) { return cfg, nil },
		checkHost:  func(config.Config, string) error { return nil },
		checkFile:  func(string, string) error { return nil },
		checkDir:   func(string, string) error { return nil },
		build: func(
			context.Context,
			snapshotbuilder.Config,
		) (snapshotbundle.Manifest, error) {
			return snapshotbundle.Manifest{}, nil
		},
		output:        output,
		processOutput: &bytes.Buffer{},
	}
}
