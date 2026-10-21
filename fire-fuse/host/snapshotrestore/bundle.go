// Package snapshotrestore validates and prepares immutable snapshot bundles
// for production VM restore.
package snapshotrestore

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/stepandel/cantelop/fire-fuse/guest-runtime/guestassignment"
	"github.com/stepandel/cantelop/fire-fuse/host/snapshotbundle"
)

const maxCPUInfoBytes = 1 << 20

var ErrIncompatible = errors.New("incompatible snapshot bundle")

// Compatibility is the request-independent host and machine identity a bundle
// must match before it can enter the restore path.
type Compatibility struct {
	FirecrackerVersion string
	CPUFingerprint     string
	VCPUCount          int
	MemoryMiB          int
	HotplugMemoryMiB   int
	KernelImage        string
	PlatformRootFS     string
}

// Bundle contains the verified manifest and host paths consumed by a restore.
type Bundle struct {
	Directory  string
	Manifest   snapshotbundle.Manifest
	StatePath  string
	MemoryPath string
}

// MatchesMachine reports whether a request has the virtual hardware shape
// serialized in this bundle.
func (b Bundle) MatchesMachine(vcpus, memoryMiB int) bool {
	return vcpus == b.Manifest.Machine.VCPUCount &&
		memoryMiB == b.Manifest.Machine.MemoryMiB
}

// LoadVerified validates a bundle's compatibility and hashes every file used
// by the restored VM.
func LoadVerified(directory string, expected Compatibility) (Bundle, error) {
	if !cleanAbsolute(directory) {
		return Bundle{}, incompatible("bundle directory must be a clean absolute path")
	}
	manifest, err := snapshotbundle.Load(filepath.Join(directory, snapshotbundle.ManifestName))
	if err != nil {
		return Bundle{}, err
	}
	if manifest.FirecrackerVersion != expected.FirecrackerVersion {
		return Bundle{}, incompatible("Firecracker version does not match")
	}
	if manifest.CPUFingerprint != expected.CPUFingerprint {
		return Bundle{}, incompatible("CPU fingerprint does not match")
	}
	if expected.VCPUCount > 0 && manifest.Machine.VCPUCount != expected.VCPUCount ||
		expected.MemoryMiB > 0 && manifest.Machine.MemoryMiB != expected.MemoryMiB ||
		expected.HotplugMemoryMiB > 0 && manifest.Machine.HotplugMemoryMiB != expected.HotplugMemoryMiB {
		return Bundle{}, incompatible("machine dimensions do not match")
	}
	if manifest.Guest.AssignmentProtocol != guestassignment.SchemaVersion {
		return Bundle{}, incompatible("guest assignment protocol does not match")
	}
	if manifest.Machine.InterfaceID != "eth0" ||
		manifest.Guest.PlatformRootPath != "/rootfs.ext4" ||
		manifest.Guest.ContainerRootPath != "/container-rootfs.ext4" ||
		manifest.Guest.VSockPath != "/run/fire-fuse.vsock" {
		return Bundle{}, incompatible("captured guest device paths do not match the runtime contract")
	}
	statePath := filepath.Join(directory, snapshotbundle.StateName)
	memoryPath := filepath.Join(directory, snapshotbundle.MemoryName)
	for _, artifact := range []struct {
		name     string
		path     string
		expected snapshotbundle.Artifact
	}{
		{name: "state", path: statePath, expected: manifest.Files.State},
		{name: "memory", path: memoryPath, expected: manifest.Files.Memory},
		{name: "kernel", path: expected.KernelImage, expected: manifest.Inputs.Kernel},
		{
			name:     "platform rootfs",
			path:     expected.PlatformRootFS,
			expected: manifest.Inputs.PlatformRootFS,
		},
	} {
		if err := verifyArtifact(artifact.path, artifact.expected); err != nil {
			return Bundle{}, fmt.Errorf("%s: %w", artifact.name, err)
		}
	}
	return Bundle{
		Directory:  directory,
		Manifest:   manifest,
		StatePath:  statePath,
		MemoryPath: memoryPath,
	}, nil
}

// FirecrackerVersion returns the exact version identity stored in manifests.
func FirecrackerVersion(ctx context.Context, binary string) (string, error) {
	// Firecracker writes its version as the first stdout line and may append a
	// normal shutdown log record to the same stream. Keep that unstable log out
	// of the compatibility identity while still requiring a successful exit.
	output, err := exec.CommandContext(ctx, binary, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("read Firecracker version: %w", err)
	}
	version, _, _ := strings.Cut(string(output), "\n")
	version = strings.TrimSpace(version)
	if !strings.HasPrefix(version, "Firecracker v") ||
		len(version) == len("Firecracker v") ||
		strings.ContainsAny(version, "\r\n\x00") {
		return "", errors.New("Firecracker returned an invalid version")
	}
	return version, nil
}

// CPUFingerprint returns a stable digest of snapshot-relevant host CPU fields.
//
// Every processor block is read, not just the first. On a hybrid host the
// performance and efficiency cores expose different flags and part numbers, so
// stopping at processor 0 would let a bundle built against one core class be
// restored onto a host whose remaining cores cannot run it. Identical entries
// are folded together, so the digest identifies the set of distinct core
// identities and does not change with core count.
func CPUFingerprint(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxCPUInfoBytes+1))
	if err != nil {
		return "", err
	}
	if len(content) > maxCPUInfoBytes {
		return "", errors.New("CPU identity data exceeds the supported size")
	}
	reader := bufio.NewScanner(bytes.NewReader(content))
	allowed := map[string]bool{
		"vendor_id": true, "cpu family": true, "model": true,
		"model name": true, "stepping": true, "microcode": true,
		"flags": true, "features": true, "cpu implementer": true,
		"cpu architecture": true, "cpu variant": true, "cpu part": true,
		"cpu revision": true,
	}
	seen := make(map[string]bool)
	var identities []string
	var values []string
	flushIdentity := func() {
		if len(values) == 0 {
			return
		}
		sort.Strings(values)
		identity := strings.Join(values, "\n")
		if !seen[identity] {
			seen[identity] = true
			identities = append(identities, identity)
		}
		values = values[:0]
	}
	for reader.Scan() {
		line := strings.TrimSpace(reader.Text())
		if line == "" {
			flushIdentity()
			continue
		}
		key, value, found := strings.Cut(line, ":")
		key = strings.ToLower(strings.TrimSpace(key))
		if !found || !allowed[key] {
			continue
		}
		value = strings.Join(strings.Fields(value), " ")
		if key == "flags" || key == "features" {
			flags := strings.Fields(value)
			sort.Strings(flags)
			value = strings.Join(flags, " ")
		}
		values = append(values, key+"="+value)
	}
	if err := reader.Err(); err != nil {
		return "", err
	}
	flushIdentity()
	if len(identities) == 0 {
		return "", errors.New("CPU identity fields are unavailable")
	}
	sort.Strings(identities)
	sum := sha256.Sum256([]byte(strings.Join(identities, "\n\n")))
	return hex.EncodeToString(sum[:]), nil
}

func verifyArtifact(path string, expected snapshotbundle.Artifact) error {
	if !cleanAbsolute(path) {
		return incompatible("artifact path must be a clean absolute path")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return err
	}
	if size != expected.SizeBytes ||
		hex.EncodeToString(hash.Sum(nil)) != expected.SHA256 {
		return ErrIncompatible
	}
	return nil
}

func cleanAbsolute(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path &&
		path != string(filepath.Separator)
}

func incompatible(detail string) error {
	return fmt.Errorf("%w: %s", ErrIncompatible, detail)
}
