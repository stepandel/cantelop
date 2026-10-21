package snapshotbundle

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

func TestCopyAndHashKeepsUnpluggedMemorySparse(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	file, err := os.Create(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(16 << 20); err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte("guest page"), 15<<20); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "copy")
	artifact, err := copyAndHash(source, destination)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.SizeBytes != 16<<20 || artifact.SHA256 == "" {
		t.Fatalf("artifact = %#v", artifact)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 16<<20 {
		t.Fatalf("copy size = %d", info.Size())
	}
	sourceInfo, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	sourceStat, sourceOK := sourceInfo.Sys().(*syscall.Stat_t)
	destinationStat, destinationOK := info.Sys().(*syscall.Stat_t)
	if sourceOK && destinationOK && sourceStat.Blocks < (8<<20)/512 && destinationStat.Blocks > (8<<20)/512 {
		t.Fatalf("snapshot copy allocated %d blocks for mostly empty memory", destinationStat.Blocks)
	}
}

func TestPublishCreatesImmutableValidatedBundle(t *testing.T) {
	root := t.TempDir()
	write := func(name, contents string) string {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	destination := filepath.Join(root, "bundle")
	createdAt := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	manifest, err := Publish(PublishInput{
		Destination:          destination,
		StateSource:          write("source-state", "state"),
		MemorySource:         write("source-memory", "memory"),
		KernelSource:         write("kernel", "kernel"),
		PlatformRootFSSource: write("rootfs", "rootfs"),
		PlaceholderSource:    write("placeholder", "placeholder"),
		FirecrackerVersion:   "Firecracker v1.15.1",
		CPUFingerprint:       digest("cpu"),
		CreatedAt:            createdAt,
		Machine: Machine{
			Architecture:     runtime.GOARCH,
			VCPUCount:        2,
			MemoryMiB:        512,
			HotplugMemoryMiB: 4096,
			GuestCID:         3,
			InterfaceID:      "eth0",
			GuestMAC:         "02:fc:ac:10:00:02",
		},
		Guest: Guest{
			AssignmentProtocol: 1,
			PlatformRootPath:   "/rootfs.ext4",
			ContainerRootPath:  "/container-rootfs.ext4",
			VSockPath:          "/run/fire-fuse.vsock",
		},
	})
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if manifest.Files.State.SHA256 != digest("state") ||
		manifest.Files.Memory.SHA256 != digest("memory") {
		t.Fatalf("snapshot hashes = %#v", manifest.Files)
	}

	loaded, err := Load(filepath.Join(destination, ManifestName))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.FirecrackerVersion != "Firecracker v1.15.1" ||
		!loaded.CreatedAt.Equal(createdAt) {
		t.Fatalf("loaded manifest = %#v", loaded)
	}
	for name, wantMode := range map[string]os.FileMode{
		ManifestName: 0o400,
		StateName:    0o444,
		MemoryName:   0o444,
	} {
		info, err := os.Stat(filepath.Join(destination, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != wantMode {
			t.Errorf("%s mode = %o, want %o", name, info.Mode().Perm(), wantMode)
		}
	}
}

func TestPublishRefusesExistingDestination(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "bundle")
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Publish(PublishInput{Destination: destination}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Publish() error = %v, want ErrInvalid", err)
	}
}

func TestManifestRejectsInvalidCompatibilityData(t *testing.T) {
	artifact := Artifact{SHA256: digest("artifact"), SizeBytes: 1}
	valid := Manifest{
		SchemaVersion:      SchemaVersion,
		CreatedAt:          time.Now().UTC(),
		FirecrackerVersion: "Firecracker v1.15.1",
		CPUFingerprint:     digest("cpu"),
		Machine: Machine{
			Architecture:     runtime.GOARCH,
			VCPUCount:        1,
			MemoryMiB:        128,
			HotplugMemoryMiB: 4096,
			GuestCID:         3,
			InterfaceID:      "eth0",
			GuestMAC:         "02:fc:ac:10:00:02",
		},
		Guest: Guest{
			AssignmentProtocol: 1,
			PlatformRootPath:   "/rootfs.ext4",
			ContainerRootPath:  "/container-rootfs.ext4",
			VSockPath:          "/run/fire-fuse.vsock",
		},
		Inputs: Inputs{
			Kernel:               artifact,
			PlatformRootFS:       artifact,
			ContainerPlaceholder: artifact,
		},
		Files: Files{State: artifact, Memory: artifact},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid manifest error = %v", err)
	}
	tests := []struct {
		name   string
		change func(*Manifest)
	}{
		{name: "schema", change: func(m *Manifest) { m.SchemaVersion++ }},
		{name: "cpu", change: func(m *Manifest) { m.CPUFingerprint = "bad" }},
		{name: "memory", change: func(m *Manifest) { m.Machine.MemoryMiB = 0 }},
		{name: "protocol", change: func(m *Manifest) { m.Guest.AssignmentProtocol = 0 }},
		{name: "path", change: func(m *Manifest) { m.Guest.VSockPath = "relative" }},
		{name: "artifact", change: func(m *Manifest) { m.Files.State.SizeBytes = 0 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := valid
			test.change(&manifest)
			if err := manifest.Validate(); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Validate() error = %v, want ErrInvalid", err)
			}
		})
	}
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
