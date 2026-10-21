package snapshotrestore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/guest-runtime/guestassignment"
	"github.com/stepandel/cantelop/fire-fuse/host/snapshotbundle"
)

func TestLoadVerifiedChecksBundleAndExternalArtifacts(t *testing.T) {
	directory, compatibility := publishTestBundle(t)
	bundle, err := LoadVerified(directory, compatibility)
	if err != nil {
		t.Fatalf("LoadVerified() error = %v", err)
	}
	if bundle.StatePath != filepath.Join(directory, snapshotbundle.StateName) ||
		bundle.MemoryPath != filepath.Join(directory, snapshotbundle.MemoryName) {
		t.Fatalf("bundle = %#v", bundle)
	}
}

func TestLoadVerifiedRejectsMismatchAndCorruption(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*testing.T, string, *Compatibility)
	}{
		{
			name: "Firecracker version",
			change: func(_ *testing.T, _ string, expected *Compatibility) {
				expected.FirecrackerVersion = "Firecracker v9"
			},
		},
		{
			name: "CPU",
			change: func(_ *testing.T, _ string, expected *Compatibility) {
				expected.CPUFingerprint = digest("other CPU")
			},
		},
		{
			name: "dimensions",
			change: func(_ *testing.T, _ string, expected *Compatibility) {
				expected.MemoryMiB++
			},
		},
		{
			name: "memory corruption",
			change: func(t *testing.T, directory string, _ *Compatibility) {
				path := filepath.Join(directory, snapshotbundle.MemoryName)
				if err := os.Chmod(path, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(
					path,
					[]byte("corrupt"),
					0o600,
				); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory, compatibility := publishTestBundle(t)
			test.change(t, directory, &compatibility)
			if _, err := LoadVerified(directory, compatibility); err == nil {
				t.Fatal("LoadVerified() error = nil")
			}
		})
	}
}

func TestCPUFingerprintNormalizesFeatureOrder(t *testing.T) {
	first := filepath.Join(t.TempDir(), "cpuinfo-1")
	second := filepath.Join(t.TempDir(), "cpuinfo-2")
	if err := os.WriteFile(
		first,
		[]byte("vendor_id : GenuineIntel\nmodel : 1\nflags : z a m\n\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		second,
		[]byte("flags: m z a\nmodel: 1\nvendor_id: GenuineIntel\n\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	firstDigest, err := CPUFingerprint(first)
	if err != nil {
		t.Fatal(err)
	}
	secondDigest, err := CPUFingerprint(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstDigest != secondDigest {
		t.Fatalf("CPU fingerprints differ: %s != %s", firstDigest, secondDigest)
	}
}

func TestFirecrackerVersionIgnoresTrailingStdoutLogging(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "firecracker")
	if err := os.WriteFile(
		binary,
		[]byte("#!/bin/sh\nprintf 'Firecracker v1.15.1\\n\\n2026-07-31 [anonymous-instance:main] Firecracker exiting successfully. exit_code=0\\n'\n"),
		0o700,
	); err != nil {
		t.Fatal(err)
	}
	version, err := FirecrackerVersion(context.Background(), binary)
	if err != nil {
		t.Fatal(err)
	}
	if version != "Firecracker v1.15.1" {
		t.Fatalf("FirecrackerVersion() = %q", version)
	}
}

func TestFirecrackerVersionIgnoresStderrLogging(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "firecracker")
	if err := os.WriteFile(
		binary,
		[]byte("#!/bin/sh\nprintf 'Firecracker v1.15.1\\n'\nprintf 'Firecracker exiting successfully.\\n' >&2\n"),
		0o700,
	); err != nil {
		t.Fatal(err)
	}
	version, err := FirecrackerVersion(context.Background(), binary)
	if err != nil {
		t.Fatal(err)
	}
	if version != "Firecracker v1.15.1" {
		t.Fatalf("FirecrackerVersion() = %q", version)
	}
}

func publishTestBundle(t *testing.T) (string, Compatibility) {
	t.Helper()
	root := t.TempDir()
	write := func(name, contents string) string {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	kernel := write("kernel", "kernel")
	platform := write("platform", "platform")
	destination := filepath.Join(root, "bundle")
	_, err := snapshotbundle.Publish(snapshotbundle.PublishInput{
		Destination:          destination,
		StateSource:          write("state", "state"),
		MemorySource:         write("memory", "memory"),
		KernelSource:         kernel,
		PlatformRootFSSource: platform,
		PlaceholderSource:    write("placeholder", "placeholder"),
		FirecrackerVersion:   "Firecracker v1.15.1",
		CPUFingerprint:       digest("cpu"),
		CreatedAt:            time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC),
		Machine: snapshotbundle.Machine{
			Architecture:     runtime.GOARCH,
			VCPUCount:        1,
			MemoryMiB:        256,
			HotplugMemoryMiB: 4096,
			GuestCID:         3,
			InterfaceID:      "eth0",
			GuestMAC:         "02:fc:00:00:00:01",
		},
		Guest: snapshotbundle.Guest{
			AssignmentProtocol: guestassignment.SchemaVersion,
			PlatformRootPath:   "/rootfs.ext4",
			ContainerRootPath:  "/container-rootfs.ext4",
			VSockPath:          "/run/fire-fuse.vsock",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return destination, Compatibility{
		FirecrackerVersion: "Firecracker v1.15.1",
		CPUFingerprint:     digest("cpu"),
		VCPUCount:          1,
		MemoryMiB:          256,
		HotplugMemoryMiB:   4096,
		KernelImage:        kernel,
		PlatformRootFS:     platform,
	}
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// A hybrid host's performance and efficiency cores differ in flags and part
// number. Reading only processor 0 would let a bundle built against one core
// class restore onto a host whose other cores cannot run it, so the digest must
// cover every block.
func TestCPUFingerprintCoversEveryProcessorBlock(t *testing.T) {
	uniform := filepath.Join(t.TempDir(), "cpuinfo-uniform")
	hybrid := filepath.Join(t.TempDir(), "cpuinfo-hybrid")
	firstBlock := "processor : 0\nvendor_id : GenuineIntel\nmodel : 1\nflags : a m z\n\n"
	if err := os.WriteFile(
		uniform,
		[]byte(firstBlock+"processor : 1\nvendor_id : GenuineIntel\nmodel : 1\nflags : a m z\n\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		hybrid,
		[]byte(firstBlock+"processor : 1\nvendor_id : GenuineIntel\nmodel : 2\nflags : a m\n\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	uniformDigest, err := CPUFingerprint(uniform)
	if err != nil {
		t.Fatal(err)
	}
	hybridDigest, err := CPUFingerprint(hybrid)
	if err != nil {
		t.Fatal(err)
	}
	if uniformDigest == hybridDigest {
		t.Fatal("hybrid and uniform hosts share a CPU fingerprint; later cores are not covered")
	}
}

// Identical cores fold together, so the digest identifies the set of distinct
// core identities and does not shift when core count changes.
func TestCPUFingerprintIgnoresCoreCount(t *testing.T) {
	block := "processor : %d\nvendor_id : GenuineIntel\nmodel : 1\nflags : a m z\n\n"
	single := filepath.Join(t.TempDir(), "cpuinfo-single")
	many := filepath.Join(t.TempDir(), "cpuinfo-many")
	if err := os.WriteFile(single, fmt.Appendf(nil, block, 0), 0o600); err != nil {
		t.Fatal(err)
	}
	content := fmt.Appendf(nil, block, 0)
	for processor := 1; processor < 8; processor++ {
		content = append(content, fmt.Appendf(nil, block, processor)...)
	}
	if err := os.WriteFile(many, content, 0o600); err != nil {
		t.Fatal(err)
	}
	singleDigest, err := CPUFingerprint(single)
	if err != nil {
		t.Fatal(err)
	}
	manyDigest, err := CPUFingerprint(many)
	if err != nil {
		t.Fatal(err)
	}
	if singleDigest != manyDigest {
		t.Fatalf("CPU fingerprint changed with core count: %s != %s", singleDigest, manyDigest)
	}
}

func TestCPUFingerprintPreservesFieldsWithinProcessorBlocks(t *testing.T) {
	first := filepath.Join(t.TempDir(), "cpuinfo-first")
	second := filepath.Join(t.TempDir(), "cpuinfo-second")
	if err := os.WriteFile(first, []byte(
		"processor : 0\nmodel : 1\nflags : a\n\n"+
			"processor : 1\nmodel : 2\nflags : b\n\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte(
		"processor : 0\nmodel : 1\nflags : b\n\n"+
			"processor : 1\nmodel : 2\nflags : a\n\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	firstDigest, err := CPUFingerprint(first)
	if err != nil {
		t.Fatal(err)
	}
	secondDigest, err := CPUFingerprint(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstDigest == secondDigest {
		t.Fatal("CPU fingerprint lost the association between fields in each processor block")
	}
}
