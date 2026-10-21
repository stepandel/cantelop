// Package snapshotbundle publishes and validates immutable Fire Fuse snapshot
// artifacts.
package snapshotbundle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const (
	SchemaVersion  = 2
	ManifestName   = "manifest.json"
	StateName      = "vmstate"
	MemoryName     = "memory"
	maxManifestLen = 64 << 10
)

var (
	ErrInvalid = errors.New("invalid snapshot bundle")
	safeValue  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:+/-]{0,255}$`)
	hexDigest  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Artifact identifies an immutable file without retaining a host-specific path.
type Artifact struct {
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
}

// Machine describes the virtual hardware serialized into the snapshot.
type Machine struct {
	Architecture     string `json:"architecture"`
	VCPUCount        int    `json:"vcpu_count"`
	MemoryMiB        int    `json:"memory_mib"`
	HotplugMemoryMiB int    `json:"hotplug_memory_mib"`
	GuestCID         uint32 `json:"guest_cid"`
	InterfaceID      string `json:"interface_id"`
	GuestMAC         string `json:"guest_mac"`
}

// Guest describes the assignment protocol and fixed jailed paths captured in
// the snapshot.
type Guest struct {
	AssignmentProtocol int    `json:"assignment_protocol"`
	PlatformRootPath   string `json:"platform_root_path"`
	ContainerRootPath  string `json:"container_root_path"`
	VSockPath          string `json:"vsock_path"`
}

// Files are the snapshot-owned files copied into the bundle.
type Files struct {
	State  Artifact `json:"state"`
	Memory Artifact `json:"memory"`
}

// Inputs are external immutable artifacts required to restore the snapshot.
type Inputs struct {
	Kernel               Artifact `json:"kernel"`
	PlatformRootFS       Artifact `json:"platform_rootfs"`
	ContainerPlaceholder Artifact `json:"container_placeholder"`
}

// Manifest is the complete compatibility and integrity contract for one
// generic snapshot bundle.
type Manifest struct {
	SchemaVersion      int       `json:"schema_version"`
	CreatedAt          time.Time `json:"created_at"`
	FirecrackerVersion string    `json:"firecracker_version"`
	CPUFingerprint     string    `json:"cpu_fingerprint"`
	Machine            Machine   `json:"machine"`
	Guest              Guest     `json:"guest"`
	Inputs             Inputs    `json:"inputs"`
	Files              Files     `json:"files"`
}

// PublishInput contains snapshot files and the compatibility metadata needed
// to publish one bundle atomically.
type PublishInput struct {
	Destination          string
	StateSource          string
	MemorySource         string
	KernelSource         string
	PlatformRootFSSource string
	PlaceholderSource    string
	FirecrackerVersion   string
	CPUFingerprint       string
	CreatedAt            time.Time
	Machine              Machine
	Guest                Guest
}

// Publish copies snapshot files and atomically publishes an immutable bundle.
func Publish(input PublishInput) (Manifest, error) {
	if err := validatePublishInput(input); err != nil {
		return Manifest{}, err
	}
	parent := filepath.Dir(input.Destination)
	if err := os.MkdirAll(parent, 0o750); err != nil {
		return Manifest{}, fmt.Errorf("create snapshot bundle parent: %w", err)
	}
	if _, err := os.Lstat(input.Destination); err == nil {
		return Manifest{}, fmt.Errorf("%w: destination already exists", ErrInvalid)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Manifest{}, fmt.Errorf("inspect snapshot bundle destination: %w", err)
	}

	staging, err := os.MkdirTemp(parent, "."+filepath.Base(input.Destination)+".tmp-")
	if err != nil {
		return Manifest{}, fmt.Errorf("create snapshot bundle staging directory: %w", err)
	}
	if err := os.Chmod(staging, 0o700); err != nil {
		os.RemoveAll(staging)
		return Manifest{}, fmt.Errorf("secure snapshot bundle staging directory: %w", err)
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(staging)
		}
	}()

	state, err := copyAndHash(input.StateSource, filepath.Join(staging, StateName))
	if err != nil {
		return Manifest{}, fmt.Errorf("publish snapshot state: %w", err)
	}
	memory, err := copyAndHash(input.MemorySource, filepath.Join(staging, MemoryName))
	if err != nil {
		return Manifest{}, fmt.Errorf("publish snapshot memory: %w", err)
	}
	kernel, err := hashFile(input.KernelSource)
	if err != nil {
		return Manifest{}, fmt.Errorf("hash kernel: %w", err)
	}
	platformRootFS, err := hashFile(input.PlatformRootFSSource)
	if err != nil {
		return Manifest{}, fmt.Errorf("hash platform rootfs: %w", err)
	}
	placeholder, err := hashFile(input.PlaceholderSource)
	if err != nil {
		return Manifest{}, fmt.Errorf("hash container placeholder: %w", err)
	}

	manifest := Manifest{
		SchemaVersion:      SchemaVersion,
		CreatedAt:          input.CreatedAt.UTC(),
		FirecrackerVersion: input.FirecrackerVersion,
		CPUFingerprint:     input.CPUFingerprint,
		Machine:            input.Machine,
		Guest:              input.Guest,
		Inputs: Inputs{
			Kernel:               kernel,
			PlatformRootFS:       platformRootFS,
			ContainerPlaceholder: placeholder,
		},
		Files: Files{State: state, Memory: memory},
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	if err := writeManifest(filepath.Join(staging, ManifestName), manifest); err != nil {
		return Manifest{}, err
	}
	if err := syncDirectory(staging); err != nil {
		return Manifest{}, fmt.Errorf("sync snapshot bundle: %w", err)
	}
	if err := os.Rename(staging, input.Destination); err != nil {
		return Manifest{}, fmt.Errorf("publish snapshot bundle: %w", err)
	}
	published = true
	if err := syncDirectory(parent); err != nil {
		return Manifest{}, fmt.Errorf("sync snapshot bundle parent: %w", err)
	}
	return manifest, nil
}

// Load reads and validates a published manifest.
func Load(path string) (Manifest, error) {
	file, err := os.Open(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("open snapshot manifest: %w", err)
	}
	defer file.Close()
	document, err := io.ReadAll(io.LimitReader(file, maxManifestLen+1))
	if err != nil {
		return Manifest{}, fmt.Errorf("read snapshot manifest: %w", err)
	}
	if len(document) > maxManifestLen {
		return Manifest{}, fmt.Errorf("%w: manifest exceeds %d bytes", ErrInvalid, maxManifestLen)
	}
	decoder := json.NewDecoder(strings.NewReader(string(document)))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("%w: decode manifest: %v", ErrInvalid, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Manifest{}, fmt.Errorf("%w: manifest must contain one JSON object", ErrInvalid)
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// Validate checks manifest structure without inspecting external files.
func (m Manifest) Validate() error {
	if m.SchemaVersion != SchemaVersion {
		return invalid("unsupported schema_version")
	}
	if m.CreatedAt.IsZero() || m.CreatedAt.Location() != time.UTC {
		return invalid("created_at must be a UTC timestamp")
	}
	if len(m.FirecrackerVersion) == 0 || len(m.FirecrackerVersion) > 256 ||
		strings.TrimSpace(m.FirecrackerVersion) != m.FirecrackerVersion ||
		strings.ContainsAny(m.FirecrackerVersion, "\x00\r\n") {
		return invalid("firecracker_version is invalid")
	}
	for name, value := range map[string]string{
		"architecture": m.Machine.Architecture,
		"interface_id": m.Machine.InterfaceID,
		"guest_mac":    m.Machine.GuestMAC,
	} {
		if !safeValue.MatchString(value) {
			return invalid(name + " is invalid")
		}
	}
	if !hexDigest.MatchString(m.CPUFingerprint) {
		return invalid("cpu_fingerprint must be a lowercase SHA-256 digest")
	}
	if m.Machine.Architecture != runtime.GOARCH {
		return invalid("architecture does not match this binary")
	}
	if m.Machine.VCPUCount < 1 || m.Machine.MemoryMiB < 1 || m.Machine.HotplugMemoryMiB < 128 || m.Machine.HotplugMemoryMiB%128 != 0 || m.Machine.GuestCID < 3 {
		return invalid("machine dimensions are invalid")
	}
	if m.Guest.AssignmentProtocol < 1 {
		return invalid("assignment protocol must be positive")
	}
	for name, value := range map[string]string{
		"platform_root_path":  m.Guest.PlatformRootPath,
		"container_root_path": m.Guest.ContainerRootPath,
		"vsock_path":          m.Guest.VSockPath,
	} {
		if !filepath.IsAbs(value) || filepath.Clean(value) != value ||
			value == string(filepath.Separator) {
			return invalid(name + " must be a clean absolute path")
		}
	}
	for name, artifact := range map[string]Artifact{
		"kernel":                m.Inputs.Kernel,
		"platform_rootfs":       m.Inputs.PlatformRootFS,
		"container_placeholder": m.Inputs.ContainerPlaceholder,
		"state":                 m.Files.State,
		"memory":                m.Files.Memory,
	} {
		if !hexDigest.MatchString(artifact.SHA256) || artifact.SizeBytes < 1 {
			return invalid(name + " artifact identity is invalid")
		}
	}
	return nil
}

func validatePublishInput(input PublishInput) error {
	if !filepath.IsAbs(input.Destination) ||
		filepath.Clean(input.Destination) != input.Destination ||
		input.Destination == string(filepath.Separator) {
		return invalid("destination must be a clean absolute path")
	}
	for name, path := range map[string]string{
		"state source":       input.StateSource,
		"memory source":      input.MemorySource,
		"kernel source":      input.KernelSource,
		"platform rootfs":    input.PlatformRootFSSource,
		"placeholder source": input.PlaceholderSource,
	} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path ||
			path == string(filepath.Separator) {
			return invalid(name + " must be a clean absolute path")
		}
	}
	if input.CreatedAt.IsZero() {
		return invalid("created_at is required")
	}
	return nil
}

func copyAndHash(sourcePath, destinationPath string) (Artifact, error) {
	source, err := openRegular(sourcePath)
	if err != nil {
		return Artifact{}, err
	}
	defer source.Close()
	destination, err := os.OpenFile(
		destinationPath,
		os.O_WRONLY|os.O_CREATE|os.O_EXCL,
		0o444,
	)
	if err != nil {
		return Artifact{}, err
	}
	hash := sha256.New()
	// Virtio-mem snapshots contain large zero-filled unplugged ranges. Preserve
	// those ranges as sparse holes instead of allocating the ceiling on disk.
	buffer := make([]byte, 1<<20)
	var size int64
	var copyErr error
	for {
		n, readErr := source.Read(buffer)
		if n > 0 {
			chunk := buffer[:n]
			_, _ = hash.Write(chunk)
			zero := true
			for _, value := range chunk {
				if value != 0 {
					zero = false
					break
				}
			}
			if zero {
				_, copyErr = destination.Seek(int64(n), io.SeekCurrent)
			} else {
				_, copyErr = destination.Write(chunk)
			}
			size += int64(n)
			if copyErr != nil {
				break
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			copyErr = readErr
			break
		}
	}
	if copyErr == nil {
		copyErr = destination.Truncate(size)
	}
	syncErr := destination.Sync()
	closeErr := destination.Close()
	if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
		return Artifact{}, err
	}
	return Artifact{SHA256: hex.EncodeToString(hash.Sum(nil)), SizeBytes: size}, nil
}

func hashFile(path string) (Artifact, error) {
	file, err := openRegular(path)
	if err != nil {
		return Artifact{}, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return Artifact{}, err
	}
	return Artifact{SHA256: hex.EncodeToString(hash.Sum(nil)), SizeBytes: size}, nil
}

func openRegular(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("source must be a regular file")
	}
	return os.Open(path)
}

func writeManifest(path string, manifest Manifest) (err error) {
	document, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode snapshot manifest: %w", err)
	}
	document = append(document, '\n')
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o400)
	if err != nil {
		return fmt.Errorf("create snapshot manifest: %w", err)
	}
	defer func() {
		if closeErr := file.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	if _, err := file.Write(document); err != nil {
		return fmt.Errorf("write snapshot manifest: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync snapshot manifest: %w", err)
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func invalid(detail string) error {
	return fmt.Errorf("%w: %s", ErrInvalid, detail)
}
