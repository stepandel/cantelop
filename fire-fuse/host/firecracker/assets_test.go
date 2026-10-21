package firecracker

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/stepandel/cantelop/fire-fuse/telemetry/warmuptrace"
)

func TestPrepareAssets(t *testing.T) {
	layout, kernelSource, rootFSSource, containerRootFSSource := testAssetLayout(t)
	var chowned []string
	var binds [][2]string
	trace := warmuptrace.New()
	ctx := warmuptrace.WithContext(context.Background(), trace)
	err := layout.prepareAssetsWithBinds(
		ctx,
		kernelSource,
		rootFSSource,
		containerRootFSSource,
		1000,
		1001,
		func(path string, uid, gid int) error {
			if uid != 1000 || gid != 1001 {
				t.Errorf("ownership = %d:%d, want 1000:1001", uid, gid)
			}
			chowned = append(chowned, path)
			return nil
		},
		func(source, destination string) error {
			binds = append(binds, [2]string{source, destination})
			return copyFile(source, destination, 0o400)
		},
		func(path string) error {
			t.Errorf("unexpected unmount of %q", path)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("prepareAssets() error = %v", err)
	}

	assertFile(t, layout.HostKernelImage, "kernel", 0o400)
	assertFile(t, layout.HostRootFSImage, "rootfs-template", 0o400)
	assertFile(t, layout.HostContainerRootFSImage, "container-rootfs-template", 0o400)
	wantBinds := [][2]string{
		{kernelSource, layout.HostKernelImage},
		{rootFSSource, layout.HostRootFSImage},
		{containerRootFSSource, layout.HostContainerRootFSImage},
	}
	if !reflect.DeepEqual(binds, wantBinds) {
		t.Errorf("binds = %#v, want %#v", binds, wantBinds)
	}
	assertFile(t, layout.RuntimeManifest, `{"schema_version":1,"owner":"fire-fuse","vm_id":"vm-test"}`+"\n", 0o600)
	if err := layout.ValidateRuntimeManifest(); err != nil {
		t.Errorf("ValidateRuntimeManifest() error = %v", err)
	}
	wantChowned := []string{
		layout.RootDir,
		filepath.Dir(layout.HostAPISocket),
	}
	if !reflect.DeepEqual(chowned, wantChowned) {
		t.Errorf("chowned paths = %#v, want %#v", chowned, wantChowned)
	}
	traceSteps := make(map[string]bool)
	for _, step := range trace.Snapshot().Steps {
		traceSteps[step.Name] = true
	}
	for _, name := range []string{
		"jail.validate_assets",
		"jail.create_vm_directory",
		"jail.write_runtime_manifest",
		"jail.bind_kernel",
		"jail.bind_platform_rootfs",
		"jail.bind_container_rootfs",
		"jail.set_asset_ownership",
	} {
		if !traceSteps[name] {
			t.Errorf("warmup trace is missing %q", name)
		}
	}

	contents, err := os.ReadFile(rootFSSource)
	if err != nil {
		t.Fatalf("ReadFile(rootfs source) error = %v", err)
	}
	if string(contents) != "rootfs-template" {
		t.Errorf("rootfs source = %q, want unchanged template", contents)
	}
}

func TestPrepareBaseAssetsThenBindsSelectedContainer(t *testing.T) {
	layout, kernelSource, rootFSSource, containerRootFSSource := testAssetLayout(t)
	bind := func(source, destination string) error {
		return copyFile(source, destination, 0o400)
	}
	if err := layout.prepareAssetsWithBinds(
		context.Background(),
		kernelSource,
		rootFSSource,
		"",
		os.Getuid(),
		os.Getgid(),
		os.Chown,
		bind,
		func(string) error { return nil },
	); err != nil {
		t.Fatalf("PrepareBaseAssetsContext() error = %v", err)
	}
	if _, err := os.Stat(layout.HostContainerRootFSImage); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("container rootfs before bind: %v", err)
	}
	if err := layout.bindContainerRootFS(
		context.Background(),
		containerRootFSSource,
		os.Getuid(),
		os.Getgid(),
		bind,
	); err != nil {
		t.Fatalf("BindContainerRootFSContext() error = %v", err)
	}
	assertFile(t, layout.HostContainerRootFSImage, "container-rootfs-template", 0o400)
	if err := layout.cleanupWithUnmount(func(string) error { return nil }); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
}

func TestPrepareAssetsRefusesExistingJail(t *testing.T) {
	layout, kernelSource, rootFSSource, containerRootFSSource := testAssetLayout(t)
	if err := os.MkdirAll(layout.VMDir, 0o750); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	sentinel := filepath.Join(layout.VMDir, "keep")
	if err := os.WriteFile(sentinel, []byte("existing"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	err := layout.prepareAssets(context.Background(), kernelSource, rootFSSource, containerRootFSSource, 1000, 1000, func(string, int, int) error { return nil })
	if !errors.Is(err, ErrJailAlreadyExists) {
		t.Fatalf("prepareAssets() error = %v, want ErrJailAlreadyExists", err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Errorf("existing jail was modified: %v", err)
	}
}

func TestPrepareAssetsCleansPartialJailOnFailure(t *testing.T) {
	layout, kernelSource, rootFSSource, containerRootFSSource := testAssetLayout(t)
	wantErr := errors.New("chown failed")

	err := layout.prepareAssets(context.Background(), kernelSource, rootFSSource, containerRootFSSource, 1000, 1000, func(string, int, int) error {
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("prepareAssets() error = %v, want chown error", err)
	}
	if _, err := os.Stat(layout.VMDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("partial VM directory still exists: %v", err)
	}
}

func TestPrepareAssetsCleansPartialJailOnBindFailure(t *testing.T) {
	layout, kernelSource, rootFSSource, containerRootFSSource := testAssetLayout(t)
	wantErr := errors.New("bind failed")
	var unmounted []string

	err := layout.prepareAssetsWithBinds(
		context.Background(),
		kernelSource,
		rootFSSource,
		containerRootFSSource,
		1000,
		1000,
		func(string, int, int) error { return nil },
		func(source, destination string) error {
			if source == containerRootFSSource {
				if destination != layout.HostContainerRootFSImage {
					t.Errorf("container bind destination = %q", destination)
				}
				return wantErr
			}
			return copyFile(source, destination, 0o400)
		},
		func(path string) error {
			unmounted = append(unmounted, path)
			return nil
		},
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("prepareAssetsWithBinds() error = %v, want bind error", err)
	}
	wantUnmounted := []string{
		layout.HostRootFSImage,
		layout.HostKernelImage,
	}
	if !reflect.DeepEqual(unmounted, wantUnmounted) {
		t.Errorf("unmounted = %#v, want %#v", unmounted, wantUnmounted)
	}
	if _, err := os.Stat(layout.VMDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("partial VM directory still exists: %v", err)
	}
}

func TestPrepareAssetsPreservesPartialJailWhenBindRollbackFails(t *testing.T) {
	layout, kernelSource, rootFSSource, containerRootFSSource := testAssetLayout(t)
	bindErr := errors.New("container bind failed")
	unmountErr := errors.New("rootfs unmount failed")

	err := layout.prepareAssetsWithBinds(
		context.Background(),
		kernelSource,
		rootFSSource,
		containerRootFSSource,
		1000,
		1000,
		func(string, int, int) error { return nil },
		func(source, destination string) error {
			if source == containerRootFSSource {
				return bindErr
			}
			return copyFile(source, destination, 0o400)
		},
		func(path string) error {
			if path == layout.HostRootFSImage {
				return unmountErr
			}
			return nil
		},
	)
	if !errors.Is(err, bindErr) || !errors.Is(err, unmountErr) {
		t.Fatalf("prepareAssetsWithBinds() error = %v, want bind and unmount errors", err)
	}
	if _, err := os.Stat(layout.VMDir); err != nil {
		t.Errorf("partial jail was removed after failed unmount: %v", err)
	}
}

func TestPrepareAssetsRejectsInvalidInputsBeforeCreatingJail(t *testing.T) {
	layout, kernelSource, rootFSSource, containerRootFSSource := testAssetLayout(t)
	tests := []struct {
		name            string
		kernel          string
		rootfs          string
		containerRootfs string
		uid             int
		gid             int
		want            error
	}{
		{name: "root UID", kernel: kernelSource, rootfs: rootFSSource, containerRootfs: containerRootFSSource, uid: 0, gid: 1000, want: ErrInvalidJailIdentity},
		{name: "root GID", kernel: kernelSource, rootfs: rootFSSource, containerRootfs: containerRootFSSource, uid: 1000, gid: 0, want: ErrInvalidJailIdentity},
		{name: "relative kernel", kernel: "vmlinux", rootfs: rootFSSource, containerRootfs: containerRootFSSource, uid: 1000, gid: 1000, want: ErrInvalidAsset},
		{name: "missing rootfs", kernel: kernelSource, rootfs: filepath.Join(filepath.Dir(rootFSSource), "missing"), containerRootfs: containerRootFSSource, uid: 1000, gid: 1000, want: ErrInvalidAsset},
		{name: "missing container rootfs", kernel: kernelSource, rootfs: rootFSSource, containerRootfs: filepath.Join(filepath.Dir(rootFSSource), "missing"), uid: 1000, gid: 1000, want: ErrInvalidAsset},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := layout.prepareAssets(context.Background(), test.kernel, test.rootfs, test.containerRootfs, test.uid, test.gid, func(string, int, int) error { return nil })
			if !errors.Is(err, test.want) {
				t.Fatalf("prepareAssets() error = %v, want %v", err, test.want)
			}
			if _, err := os.Stat(layout.VMDir); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("VM directory was created: %v", err)
			}
		})
	}
}

func TestPrepareAssetsRejectsArtifactsUnreadableByJail(t *testing.T) {
	for _, assetName := range []string{"kernel", "rootfs", "container rootfs"} {
		t.Run(assetName, func(t *testing.T) {
			layout, kernelSource, rootFSSource, containerRootFSSource := testAssetLayout(t)
			path := map[string]string{
				"kernel":           kernelSource,
				"rootfs":           rootFSSource,
				"container rootfs": containerRootFSSource,
			}[assetName]
			if err := os.Chmod(path, 0o600); err != nil {
				t.Fatal(err)
			}

			err := layout.prepareAssets(
				context.Background(),
				kernelSource,
				rootFSSource,
				containerRootFSSource,
				1000,
				1000,
				func(string, int, int) error { return nil },
			)
			if !errors.Is(err, ErrInvalidAsset) {
				t.Fatalf("prepareAssets() error = %v, want ErrInvalidAsset", err)
			}
			if _, err := os.Stat(layout.VMDir); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("VM directory was created: %v", err)
			}
		})
	}
}

func TestCleanupRemovesOnlyVMDirectory(t *testing.T) {
	layout, kernelSource, rootFSSource, containerRootFSSource := testAssetLayout(t)
	if err := layout.prepareAssetsWithBinds(
		context.Background(),
		kernelSource,
		rootFSSource,
		containerRootFSSource,
		1000,
		1000,
		func(string, int, int) error { return nil },
		func(source, destination string) error {
			return copyFile(source, destination, 0o400)
		},
		func(string) error { return nil },
	); err != nil {
		t.Fatalf("prepareAssets() error = %v", err)
	}
	sibling := filepath.Join(filepath.Dir(layout.VMDir), "keep")
	if err := os.WriteFile(sibling, []byte("sibling"), 0o600); err != nil {
		t.Fatalf("WriteFile(sibling) error = %v", err)
	}

	var unmounted []string
	if err := layout.cleanupWithUnmount(func(path string) error {
		unmounted = append(unmounted, path)
		return nil
	}); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	wantUnmounted := []string{
		layout.HostSnapshotMemory,
		layout.HostSnapshotState,
		layout.HostContainerRootFSImage,
		layout.HostRootFSImage,
		layout.HostKernelImage,
	}
	if !reflect.DeepEqual(unmounted, wantUnmounted) {
		t.Errorf("unmounted = %#v, want %#v", unmounted, wantUnmounted)
	}
	if _, err := os.Stat(layout.VMDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("VM directory still exists: %v", err)
	}
	if _, err := os.Stat(sibling); err != nil {
		t.Errorf("sibling was removed: %v", err)
	}
}

func TestCleanupPreservesJailWhenUnmountFails(t *testing.T) {
	layout, _, _, _ := testAssetLayout(t)
	if err := os.MkdirAll(layout.RootDir, 0o750); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("unmount failed")
	var unmounted []string

	err := layout.cleanupWithUnmount(func(path string) error {
		unmounted = append(unmounted, path)
		if path == layout.HostRootFSImage {
			return wantErr
		}
		return nil
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("cleanupWithUnmount() error = %v, want unmount error", err)
	}
	if _, err := os.Stat(layout.VMDir); err != nil {
		t.Errorf("jail was removed after failed unmount: %v", err)
	}
	wantUnmounted := []string{
		layout.HostSnapshotMemory,
		layout.HostSnapshotState,
		layout.HostContainerRootFSImage,
		layout.HostRootFSImage,
		layout.HostKernelImage,
	}
	if !reflect.DeepEqual(unmounted, wantUnmounted) {
		t.Errorf("unmounted = %#v, want %#v", unmounted, wantUnmounted)
	}
}

func TestCleanupRejectsModifiedLayout(t *testing.T) {
	layout, _, _, _ := testAssetLayout(t)
	if err := os.MkdirAll(layout.ChrootBaseDir, 0o750); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	sentinel := filepath.Join(layout.ChrootBaseDir, "keep")
	if err := os.WriteFile(sentinel, []byte("sentinel"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	layout.VMDir = layout.ChrootBaseDir

	if err := layout.Cleanup(); !errors.Is(err, ErrInvalidJailLayout) {
		t.Fatalf("Cleanup() error = %v, want ErrInvalidJailLayout", err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Errorf("sentinel was removed: %v", err)
	}
}

func testAssetLayout(t *testing.T) (JailLayout, string, string, string) {
	t.Helper()

	tempDir, err := os.MkdirTemp("/tmp", "fire-fuse-assets-")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tempDir) })
	imagesDir := filepath.Join(tempDir, "images")
	if err := os.Mkdir(imagesDir, 0o750); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	kernelSource := filepath.Join(imagesDir, "vmlinux")
	rootFSSource := filepath.Join(imagesDir, "rootfs.ext4")
	containerRootFSSource := filepath.Join(imagesDir, "container-rootfs.ext4")
	if err := os.WriteFile(kernelSource, []byte("kernel"), 0o644); err != nil {
		t.Fatalf("WriteFile(kernel) error = %v", err)
	}
	if err := os.WriteFile(rootFSSource, []byte("rootfs-template"), 0o644); err != nil {
		t.Fatalf("WriteFile(rootfs) error = %v", err)
	}
	if err := os.WriteFile(containerRootFSSource, []byte("container-rootfs-template"), 0o644); err != nil {
		t.Fatalf("WriteFile(container rootfs) error = %v", err)
	}
	layout, err := NewJailLayout(filepath.Join(tempDir, "jails"), "/usr/bin/firecracker", "vm-test")
	if err != nil {
		t.Fatalf("NewJailLayout() error = %v", err)
	}
	return layout, kernelSource, rootFSSource, containerRootFSSource
}

func assertFile(t *testing.T, path, wantContents string, wantMode os.FileMode) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", path, err)
	}
	if string(contents) != wantContents {
		t.Errorf("contents of %s = %q, want %q", path, contents, wantContents)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%s) error = %v", path, err)
	}
	if got := info.Mode().Perm(); got != wantMode {
		t.Errorf("mode of %s = %o, want %o", path, got, wantMode)
	}
}

func copyFile(sourcePath, destinationPath string, mode os.FileMode) (err error) {
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()

	destination, err := os.OpenFile(destinationPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := destination.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
		if err != nil {
			_ = os.Remove(destinationPath)
		}
	}()

	if _, err = io.Copy(destination, source); err != nil {
		return err
	}
	if err = destination.Sync(); err != nil {
		return err
	}
	if err = destination.Chmod(mode); err != nil {
		return err
	}
	return nil
}
