package firecracker

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// PrepareSnapshotOutput creates the jailed directory in which Firecracker
// writes a template's state and memory files.
func (l JailLayout) PrepareSnapshotOutput(uid, gid int) error {
	return l.prepareSnapshotOutput(uid, gid, os.Chown)
}

func (l JailLayout) prepareSnapshotOutput(
	uid, gid int,
	chown func(string, int, int) error,
) error {
	if err := l.validate(); err != nil {
		return err
	}
	if uid < 1 || gid < 1 {
		return ErrInvalidJailIdentity
	}
	if err := os.Mkdir(l.HostSnapshotDirectory, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%w: snapshot output already exists", ErrJailAlreadyExists)
		}
		return fmt.Errorf("create snapshot output directory: %w", err)
	}
	if err := chown(l.HostSnapshotDirectory, uid, gid); err != nil {
		_ = os.Remove(l.HostSnapshotDirectory)
		return fmt.Errorf("set snapshot output ownership: %w", err)
	}
	return nil
}

// PrepareRestoreInputs bind-mounts immutable snapshot state and memory into an
// already-prepared jail.
func (l JailLayout) PrepareRestoreInputs(
	ctx context.Context,
	stateSource,
	memorySource string,
	uid,
	gid int,
) error {
	return l.prepareRestoreInputs(
		ctx,
		stateSource,
		memorySource,
		uid,
		gid,
		os.Chown,
		bindReadOnlyFile,
		unmountReadOnlyFile,
	)
}

func (l JailLayout) prepareRestoreInputs(
	_ context.Context,
	stateSource,
	memorySource string,
	uid,
	gid int,
	chown chownFunc,
	bindReadOnly bindReadOnlyFunc,
	unmount unmountFunc,
) (err error) {
	if err := l.validate(); err != nil {
		return err
	}
	if uid < 1 || gid < 1 {
		return ErrInvalidJailIdentity
	}
	for _, asset := range []struct {
		name string
		path string
	}{
		{name: "snapshot state", path: stateSource},
		{name: "snapshot memory", path: memorySource},
	} {
		if err := validateAssetSource(asset.name, asset.path); err != nil {
			return err
		}
		if err := validateAssetReadableByIdentity(asset.name, asset.path, uid, gid); err != nil {
			return err
		}
	}
	if err := os.Mkdir(l.HostSnapshotDirectory, 0o750); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%w: snapshot input directory already exists", ErrJailAlreadyExists)
		}
		return fmt.Errorf("create snapshot input directory: %w", err)
	}
	if err := chown(l.HostSnapshotDirectory, uid, gid); err != nil {
		_ = os.Remove(l.HostSnapshotDirectory)
		return fmt.Errorf("set snapshot input ownership: %w", err)
	}
	var boundPaths []string
	defer func() {
		if err == nil {
			return
		}
		unmountFailed := false
		for index := len(boundPaths) - 1; index >= 0; index-- {
			if cleanupErr := unmount(boundPaths[index]); cleanupErr != nil {
				unmountFailed = true
				err = errors.Join(
					err,
					fmt.Errorf("unmount partial snapshot input %s: %w", boundPaths[index], cleanupErr),
				)
			}
		}
		if !unmountFailed {
			_ = os.RemoveAll(l.HostSnapshotDirectory)
		}
	}()
	for _, asset := range []struct {
		name        string
		source      string
		destination string
	}{
		{
			name:        "snapshot state",
			source:      stateSource,
			destination: l.HostSnapshotState,
		},
		{
			name:        "snapshot memory",
			source:      memorySource,
			destination: l.HostSnapshotMemory,
		},
	} {
		if err := bindReadOnly(asset.source, asset.destination); err != nil {
			return fmt.Errorf("bind %s into jail: %w", asset.name, err)
		}
		boundPaths = append(boundPaths, asset.destination)
	}
	return nil
}
