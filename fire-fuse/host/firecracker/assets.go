package firecracker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/stepandel/cantelop/fire-fuse/telemetry/warmuptrace"
)

var (
	ErrInvalidJailIdentity = errors.New("invalid Firecracker jail identity")
	ErrInvalidAsset        = errors.New("invalid Firecracker jail asset")
	ErrJailAlreadyExists   = errors.New("Firecracker jail already exists")
)

type chownFunc func(string, int, int) error
type bindReadOnlyFunc func(string, string) error
type unmountFunc func(string) error

// PrepareAssetsContext creates a new jail, bind-mounts its immutable boot
// artifacts read-only, and records each preparation step.
func (l JailLayout) PrepareAssetsContext(
	ctx context.Context,
	kernelSource,
	rootFSSource,
	containerRootFSSource string,
	uid,
	gid int,
) error {
	return l.prepareAssets(ctx, kernelSource, rootFSSource, containerRootFSSource, uid, gid, os.Chown)
}

// PrepareBaseAssetsContext creates the request-independent portion of a jail.
// The selected container rootfs must be attached with BindContainerRootFSContext
// before Firecracker is launched.
func (l JailLayout) PrepareBaseAssetsContext(
	ctx context.Context,
	kernelSource,
	rootFSSource string,
	uid,
	gid int,
) error {
	return l.prepareAssets(ctx, kernelSource, rootFSSource, "", uid, gid, os.Chown)
}

// BindContainerRootFSContext attaches one request-selected immutable image to
// an already prepared base jail.
func (l JailLayout) BindContainerRootFSContext(
	ctx context.Context,
	containerRootFSSource string,
	uid,
	gid int,
) error {
	return l.bindContainerRootFS(ctx, containerRootFSSource, uid, gid, bindReadOnlyFile)
}

func (l JailLayout) bindContainerRootFS(
	ctx context.Context,
	containerRootFSSource string,
	uid,
	gid int,
	bindReadOnly bindReadOnlyFunc,
) error {
	finishValidation := warmuptrace.Start(ctx, "jail.validate_container_rootfs")
	if err := l.validate(); err != nil {
		finishValidation()
		return err
	}
	if uid < 1 || gid < 1 {
		finishValidation()
		return ErrInvalidJailIdentity
	}
	if err := validateAssetSource("container rootfs", containerRootFSSource); err != nil {
		finishValidation()
		return err
	}
	if err := validateAssetReadableByIdentity("container rootfs", containerRootFSSource, uid, gid); err != nil {
		finishValidation()
		return err
	}
	if err := l.ValidateRuntimeManifest(); err != nil {
		finishValidation()
		return fmt.Errorf("%w: base jail manifest: %v", ErrInvalidJailLayout, err)
	}
	if info, err := os.Stat(l.RootDir); err != nil || !info.IsDir() {
		finishValidation()
		return fmt.Errorf("%w: base jail is not prepared", ErrInvalidJailLayout)
	}
	finishValidation()

	finishBind := warmuptrace.Start(ctx, "jail.bind_container_rootfs")
	if err := bindReadOnly(containerRootFSSource, l.HostContainerRootFSImage); err != nil {
		finishBind()
		return fmt.Errorf("bind container rootfs into jail: %w", err)
	}
	finishBind()
	return nil
}

func (l JailLayout) prepareAssets(
	ctx context.Context,
	kernelSource string,
	rootFSSource string,
	containerRootFSSource string,
	uid int,
	gid int,
	chown chownFunc,
) (err error) {
	return l.prepareAssetsWithBinds(
		ctx,
		kernelSource,
		rootFSSource,
		containerRootFSSource,
		uid,
		gid,
		chown,
		bindReadOnlyFile,
		unmountReadOnlyFile,
	)
}

func (l JailLayout) prepareAssetsWithBinds(
	ctx context.Context,
	kernelSource string,
	rootFSSource string,
	containerRootFSSource string,
	uid int,
	gid int,
	chown chownFunc,
	bindReadOnly bindReadOnlyFunc,
	unmount unmountFunc,
) (err error) {
	finishValidation := warmuptrace.Start(ctx, "jail.validate_assets")
	if err := l.validate(); err != nil {
		finishValidation()
		return err
	}
	if uid < 1 || gid < 1 {
		finishValidation()
		return ErrInvalidJailIdentity
	}
	if err := validateAssetSource("kernel", kernelSource); err != nil {
		finishValidation()
		return err
	}
	if err := validateAssetSource("rootfs", rootFSSource); err != nil {
		finishValidation()
		return err
	}
	if containerRootFSSource != "" {
		if err := validateAssetSource("container rootfs", containerRootFSSource); err != nil {
			finishValidation()
			return err
		}
	}
	assets := []struct {
		name string
		path string
	}{
		{name: "kernel", path: kernelSource},
		{name: "rootfs", path: rootFSSource},
	}
	if containerRootFSSource != "" {
		assets = append(assets, struct {
			name string
			path string
		}{name: "container rootfs", path: containerRootFSSource})
	}
	for _, asset := range assets {
		if err := validateAssetReadableByIdentity(asset.name, asset.path, uid, gid); err != nil {
			finishValidation()
			return err
		}
	}
	finishValidation()

	finishParent := warmuptrace.Start(ctx, "jail.create_parent_directory")
	if err := os.MkdirAll(filepath.Dir(l.VMDir), 0o750); err != nil {
		finishParent()
		return fmt.Errorf("create jail parent: %w", err)
	}
	finishParent()
	finishVMDirectory := warmuptrace.Start(ctx, "jail.create_vm_directory")
	if err := os.Mkdir(l.VMDir, 0o750); err != nil {
		finishVMDirectory()
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%w: %s", ErrJailAlreadyExists, l.VMDir)
		}
		return fmt.Errorf("create VM jail directory: %w", err)
	}
	finishVMDirectory()
	created := true
	var boundPaths []string
	defer func() {
		if err == nil || !created {
			return
		}
		unmountFailed := false
		for index := len(boundPaths) - 1; index >= 0; index-- {
			if cleanupErr := unmount(boundPaths[index]); cleanupErr != nil {
				unmountFailed = true
				err = errors.Join(
					err,
					fmt.Errorf(
						"unmount partial jail asset %s: %w",
						boundPaths[index],
						cleanupErr,
					),
				)
			}
		}
		if unmountFailed {
			return
		}
		if cleanupErr := os.RemoveAll(l.VMDir); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("clean up partial jail: %w", cleanupErr))
		}
	}()
	finishManifest := warmuptrace.Start(ctx, "jail.write_runtime_manifest")
	if err := l.createRuntimeManifest(); err != nil {
		finishManifest()
		return err
	}
	finishManifest()

	runDir := filepath.Dir(l.HostAPISocket)
	finishDirectories := warmuptrace.Start(ctx, "jail.create_runtime_directories")
	if err := os.MkdirAll(runDir, 0o750); err != nil {
		finishDirectories()
		return fmt.Errorf("create jail directories: %w", err)
	}
	finishDirectories()
	for _, directory := range []string{l.RootDir, runDir} {
		finishPermissions := warmuptrace.Start(ctx, "jail.set_directory_permissions")
		if err := os.Chmod(directory, 0o750); err != nil {
			finishPermissions()
			return fmt.Errorf("set jail directory permissions: %w", err)
		}
		finishPermissions()
	}

	for _, path := range []string{
		l.RootDir,
		runDir,
	} {
		finishOwnership := warmuptrace.Start(ctx, "jail.set_asset_ownership")
		if err := chown(path, uid, gid); err != nil {
			finishOwnership()
			return fmt.Errorf("set jail ownership on %s: %w", path, err)
		}
		finishOwnership()
	}

	boundAssets := []struct {
		name        string
		trace       string
		source      string
		destination string
	}{
		{
			name:        "kernel",
			trace:       "jail.bind_kernel",
			source:      kernelSource,
			destination: l.HostKernelImage,
		},
		{
			name:        "platform rootfs",
			trace:       "jail.bind_platform_rootfs",
			source:      rootFSSource,
			destination: l.HostRootFSImage,
		},
	}
	if containerRootFSSource != "" {
		boundAssets = append(boundAssets, struct {
			name        string
			trace       string
			source      string
			destination string
		}{
			name:        "container rootfs",
			trace:       "jail.bind_container_rootfs",
			source:      containerRootFSSource,
			destination: l.HostContainerRootFSImage,
		})
	}
	for _, asset := range boundAssets {
		finishBind := warmuptrace.Start(ctx, asset.trace)
		if err := bindReadOnly(asset.source, asset.destination); err != nil {
			finishBind()
			return fmt.Errorf("bind %s into jail: %w", asset.name, err)
		}
		finishBind()
		boundPaths = append(boundPaths, asset.destination)
	}

	return nil
}

// Cleanup removes only this layout's validated VM-specific directory.
// The caller must ensure the jailer and Firecracker processes have exited.
func (l JailLayout) Cleanup() error {
	return l.cleanupWithUnmount(unmountReadOnlyFile)
}

func (l JailLayout) cleanupWithUnmount(unmount unmountFunc) error {
	if err := l.validate(); err != nil {
		return err
	}
	var unmountErrors []error
	for _, asset := range []struct {
		name string
		path string
	}{
		{name: "snapshot memory", path: l.HostSnapshotMemory},
		{name: "snapshot state", path: l.HostSnapshotState},
		{name: "container rootfs", path: l.HostContainerRootFSImage},
		{name: "platform rootfs", path: l.HostRootFSImage},
		{name: "kernel", path: l.HostKernelImage},
	} {
		if err := unmount(asset.path); err != nil {
			unmountErrors = append(
				unmountErrors,
				fmt.Errorf("unmount %s from jail: %w", asset.name, err),
			)
		}
	}
	if err := errors.Join(unmountErrors...); err != nil {
		return err
	}
	if err := os.RemoveAll(l.VMDir); err != nil {
		return fmt.Errorf("remove VM jail directory: %w", err)
	}
	return nil
}

func validateAssetReadableByIdentity(name, path string, uid, gid int) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%w: %s: %v", ErrInvalidAsset, name, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%w: %s ownership is unavailable", ErrInvalidAsset, name)
	}
	mode := info.Mode().Perm()
	readable := mode&0o004 != 0 ||
		int(stat.Uid) == uid && mode&0o400 != 0 ||
		int(stat.Gid) == gid && mode&0o040 != 0
	if !readable {
		return fmt.Errorf(
			"%w: %s must be readable by jail identity %d:%d",
			ErrInvalidAsset,
			name,
			uid,
			gid,
		)
	}
	return nil
}

func validateAssetSource(name, path string) error {
	if err := validateCleanAbsolutePath(path); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrInvalidAsset, name, err)
	}
	if path == string(filepath.Separator) {
		return fmt.Errorf("%w: %s cannot be the filesystem root", ErrInvalidAsset, name)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%w: %s: %v", ErrInvalidAsset, name, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: %s must be a regular file", ErrInvalidAsset, name)
	}
	return nil
}
