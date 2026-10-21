// Package imagecatalog publishes immutable workload images into the trusted
// host catalog used for new VM requests.
package imagecatalog

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/stepandel/cantelop/fire-fuse/config"
	"github.com/stepandel/cantelop/fire-fuse/host/hostpath"
)

const (
	manifestSchemaVersion = 1
	imageRoot             = "/var/lib/fire-fuse/images"
)

var ErrCatalogDeployment = errors.New("image catalog is locked by runtime deployment")

// Manifest is the reviewed handoff from an external image materializer. The
// image ID is deliberately derived instead of accepted from the caller.
type Manifest struct {
	SchemaVersion int                   `json:"schema_version"`
	Image         config.ContainerImage `json:"image"`
}

// Result describes an idempotent catalog publication.
type Result struct {
	BuildID string
	Changed bool
}

// PublishImage publishes a worker-materialized image without an intermediate
// manifest file. The image and its installed rootfs still cross the same trust,
// content-addressing, and catalog-lock boundary as the operator command.
func PublishImage(catalogPath string, image config.ContainerImage) (result Result, err error) {
	if os.Geteuid() != 0 {
		return Result{}, errors.New("image catalog publication requires root")
	}
	return publishInstalledImage(catalogPath, image, acquireLock, validateInstalledImage, config.LoadTrustedImageCatalog)
}

// Publish verifies an immutable Build and adds it to the trusted host catalog.
// Apps select the Build directly when they are created.
func Publish(catalogPath, manifestPath string) (result Result, err error) {
	if os.Geteuid() != 0 {
		return Result{}, errors.New("image catalog publication requires root")
	}
	if err := hostpath.CheckTrustedFile("image_manifest", manifestPath); err != nil {
		return Result{}, fmt.Errorf("validate image manifest trust: %w", err)
	}
	manifest, err := LoadManifest(manifestPath)
	if err != nil {
		return Result{}, err
	}
	return publishInstalledImage(catalogPath, manifest.Image, acquireLock, validateInstalledImage, config.LoadTrustedImageCatalog)
}

// Validate the rootfs while holding the same lock as collection, so a collector
// cannot unlink a verified file before its catalog entry is published.
func publishInstalledImage(
	catalogPath string,
	image config.ContainerImage,
	lockCatalog func(string) (*os.File, error),
	validateImage func(config.ContainerImage) error,
	loadCatalog func(string) (map[string]config.ContainerImage, error),
) (result Result, err error) {
	lock, err := lockCatalog(catalogPath + ".lock")
	if err != nil {
		return Result{}, err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	if err := validateImage(image); err != nil {
		return Result{}, err
	}
	images, err := loadCatalog(catalogPath)
	if err != nil {
		return Result{}, fmt.Errorf("load current image catalog: %w", err)
	}
	return publish(catalogPath, images, image)
}

// LoadManifest strictly decodes one external materializer handoff.
func LoadManifest(path string) (Manifest, error) {
	file, err := os.Open(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("open image manifest: %w", err)
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode image manifest: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Manifest{}, errors.New("image manifest must contain exactly one JSON object")
	}
	if manifest.SchemaVersion != manifestSchemaVersion {
		return Manifest{}, fmt.Errorf(
			"image manifest schema_version is %d, want %d",
			manifest.SchemaVersion,
			manifestSchemaVersion,
		)
	}
	if _, err := config.ContainerImageID(manifest.Image); err != nil {
		return Manifest{}, fmt.Errorf("validate image manifest: %w", err)
	}
	return manifest, nil
}

func validateInstalledImage(image config.ContainerImage) error {
	wantPath := filepath.Join(imageRoot, "sha256-"+image.RootFSSHA256+".ext4")
	if image.RootFSImage != wantPath {
		return fmt.Errorf("rootfs_image is %q, want immutable path %q", image.RootFSImage, wantPath)
	}
	if err := hostpath.CheckTrustedFile("image_rootfs", image.RootFSImage); err != nil {
		return fmt.Errorf("validate image rootfs trust: %w", err)
	}
	file, err := os.Open(image.RootFSImage)
	if err != nil {
		return fmt.Errorf("open image rootfs: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("inspect image rootfs: %w", err)
	}
	if info.Size() != image.RootFSSizeBytes {
		return fmt.Errorf("image rootfs size is %d, want %d", info.Size(), image.RootFSSizeBytes)
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return fmt.Errorf("hash image rootfs: %w", err)
	}
	gotDigest := fmt.Sprintf("%x", digest.Sum(nil))
	if gotDigest != image.RootFSSHA256 {
		return fmt.Errorf("image rootfs SHA-256 is %s, want %s", gotDigest, image.RootFSSHA256)
	}
	return nil
}

func publish(
	catalogPath string,
	images map[string]config.ContainerImage,
	image config.ContainerImage,
) (Result, error) {
	buildID, err := config.ContainerImageID(image)
	if err != nil {
		return Result{}, fmt.Errorf("derive build ID: %w", err)
	}
	catalogChanged := false
	if existing, exists := images[buildID]; exists {
		if !reflect.DeepEqual(existing, image) {
			return Result{}, fmt.Errorf("build ID %s already has different catalog metadata", buildID)
		}
	} else {
		next := make(map[string]config.ContainerImage, len(images)+1)
		for existingID, existing := range images {
			next[existingID] = existing
		}
		next[buildID] = image
		if err := writeAtomic(catalogPath, config.ImageCatalogDocument{
			SchemaVersion: config.ImageCatalogSchemaVersion,
			Images:        next,
		}); err != nil {
			return Result{}, err
		}
		catalogChanged = true
	}

	return Result{BuildID: buildID, Changed: catalogChanged}, nil
}

func acquireLock(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open image catalog lock: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("create image catalog lock handle")
	}
	fail := func(err error) (*os.File, error) {
		return nil, errors.Join(err, file.Close())
	}
	info, err := file.Stat()
	if err != nil {
		return fail(fmt.Errorf("inspect image catalog lock: %w", err))
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 || !info.Mode().IsRegular() {
		return fail(errors.New("image catalog lock must be a root-owned regular file"))
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fail(fmt.Errorf("image catalog lock mode is %04o, want 0600", info.Mode().Perm()))
	}
	if err := unix.Flock(fd, unix.LOCK_EX); err != nil {
		return fail(fmt.Errorf("lock image catalog: %w", err))
	}
	if err := checkDeploymentLease(path); err != nil {
		return fail(err)
	}
	return file, nil
}

func checkDeploymentLease(lockPath string) error {
	leasePath := lockPath + ".deployment"
	if _, err := os.Lstat(leasePath); err == nil {
		return fmt.Errorf("%w: %s", ErrCatalogDeployment, leasePath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect image catalog deployment lease: %w", err)
	}
	return nil
}

func writeAtomic(path string, document config.ImageCatalogDocument) error {
	contents, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return fmt.Errorf("encode image catalog: %w", err)
	}
	contents = append(contents, '\n')

	temporary, err := os.CreateTemp(filepath.Dir(path), ".image-catalog-*")
	if err != nil {
		return fmt.Errorf("create temporary image catalog: %w", err)
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o640); err != nil {
		return fmt.Errorf("set temporary image catalog mode: %w", err)
	}
	if os.Geteuid() == 0 {
		if err := temporary.Chown(0, 0); err != nil {
			return fmt.Errorf("set temporary image catalog ownership: %w", err)
		}
	}
	if _, err := temporary.Write(contents); err != nil {
		return fmt.Errorf("write temporary image catalog: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync temporary image catalog: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary image catalog: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace image catalog: %w", err)
	}
	committed = true
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("open image catalog directory: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync image catalog directory: %w", err)
	}
	return nil
}
