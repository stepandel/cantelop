package firecracker

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

const (
	runtimeManifestFilename = "fire-fuse-runtime.json"
	runtimeManifestVersion  = 1
	runtimeManifestOwner    = "fire-fuse"
	runtimeManifestMaxBytes = 4096
)

var ErrInvalidRuntimeManifest = errors.New("invalid fire-fuse runtime manifest")

type runtimeManifest struct {
	SchemaVersion int    `json:"schema_version"`
	Owner         string `json:"owner"`
	VMID          string `json:"vm_id"`
}

func (l JailLayout) createRuntimeManifest() (err error) {
	manifest := runtimeManifest{
		SchemaVersion: runtimeManifestVersion,
		Owner:         runtimeManifestOwner,
		VMID:          l.VMID,
	}
	record, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("encode runtime manifest: %w", err)
	}
	record = append(record, '\n')

	file, err := os.OpenFile(l.RuntimeManifest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create runtime manifest: %w", err)
	}
	defer func() {
		if closeErr := file.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close runtime manifest: %w", closeErr)
		}
	}()
	if _, err := file.Write(record); err != nil {
		return fmt.Errorf("write runtime manifest: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync runtime manifest: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		return fmt.Errorf("secure runtime manifest: %w", err)
	}
	return nil
}

// ValidateRuntimeManifest strictly verifies this VM directory's ownership marker.
func (l JailLayout) ValidateRuntimeManifest() error {
	if err := l.validate(); err != nil {
		return err
	}
	fd, err := unix.Open(l.RuntimeManifest, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("%w: open manifest: %v", ErrInvalidRuntimeManifest, err)
	}
	file := os.NewFile(uintptr(fd), l.RuntimeManifest)
	if file == nil {
		_ = unix.Close(fd)
		return fmt.Errorf("%w: create manifest file handle", ErrInvalidRuntimeManifest)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("%w: inspect manifest: %v", ErrInvalidRuntimeManifest, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: manifest is not a regular file", ErrInvalidRuntimeManifest)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%w: manifest permissions are too broad", ErrInvalidRuntimeManifest)
	}
	record, err := io.ReadAll(io.LimitReader(file, runtimeManifestMaxBytes+1))
	if err != nil {
		return fmt.Errorf("%w: read manifest: %v", ErrInvalidRuntimeManifest, err)
	}
	if len(record) > runtimeManifestMaxBytes {
		return fmt.Errorf("%w: manifest exceeds %d bytes", ErrInvalidRuntimeManifest, runtimeManifestMaxBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(record))
	decoder.DisallowUnknownFields()
	var manifest runtimeManifest
	if err := decoder.Decode(&manifest); err != nil {
		return fmt.Errorf("%w: decode JSON: %v", ErrInvalidRuntimeManifest, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: manifest must contain exactly one JSON object", ErrInvalidRuntimeManifest)
	}
	if manifest.SchemaVersion != runtimeManifestVersion ||
		manifest.Owner != runtimeManifestOwner ||
		manifest.VMID != l.VMID {
		return fmt.Errorf("%w: ownership identity does not match", ErrInvalidRuntimeManifest)
	}
	return nil
}
