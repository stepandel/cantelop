package imagecatalog

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"github.com/stepandel/cantelop/fire-fuse/config"
	"github.com/stepandel/cantelop/fire-fuse/host/hostpath"
	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
)

var (
	ErrRootFSInUse           = errors.New("rootfs is referenced by published images")
	ErrUnresolvedActiveImage = errors.New("non-terminated microVM references an unpublished image")
	ErrUnresolvedAppBuild    = errors.New("App references an unpublished build")
	rootFSNamePattern        = regexp.MustCompile(`^sha256-([0-9a-f]{64})\.ext4$`)
)

// CollectionResult describes idempotent deletion of one explicitly selected,
// unpublished immutable rootfs.
type CollectionResult struct {
	RootFSImage string
	Changed     bool
}

// CollectRootFS proves that one digest-derived rootfs is not reachable from
// the catalog or any non-terminated VM, then deletes exactly that file. It
// requires fire-fuse to be stopped so lifecycle references cannot race the
// proof.
func CollectRootFS(catalogPath, statePath, rootfsPath string) (result CollectionResult, err error) {
	if os.Geteuid() != 0 {
		return CollectionResult{}, errors.New("image rootfs collection requires root")
	}
	expectedDigest, err := validateRootFSPath(rootfsPath)
	if err != nil {
		return CollectionResult{}, err
	}
	if err := hostpath.CheckTrustedDirectory("image_root", imageRoot); err != nil {
		return CollectionResult{}, fmt.Errorf("validate image root trust: %w", err)
	}
	if err := hostpath.CheckTrustedFile("state_database", statePath); err != nil {
		return CollectionResult{}, fmt.Errorf("validate lifecycle state trust: %w", err)
	}
	store, err := microvm.OpenSQLiteStore(statePath)
	if err != nil {
		return CollectionResult{}, fmt.Errorf("acquire exclusive lifecycle state: %w", err)
	}
	defer func() {
		err = errors.Join(err, store.Close())
	}()

	lock, err := acquireLock(catalogPath + ".lock")
	if err != nil {
		return CollectionResult{}, err
	}
	defer func() {
		err = errors.Join(err, lock.Close())
	}()

	images, err := config.LoadTrustedImageCatalog(catalogPath)
	if err != nil {
		return CollectionResult{}, fmt.Errorf("load current image catalog: %w", err)
	}
	machines, err := store.ListActive()
	if err != nil {
		return CollectionResult{}, fmt.Errorf("list persisted microVMs: %w", err)
	}
	builds, err := listRegisteredBuilds(store)
	if err != nil {
		return CollectionResult{}, err
	}
	if err := ensureRootFSUnreferenced(images, builds, machines, rootfsPath); err != nil {
		return CollectionResult{}, err
	}

	info, err := os.Lstat(rootfsPath)
	if errors.Is(err, os.ErrNotExist) {
		return CollectionResult{RootFSImage: rootfsPath}, nil
	}
	if err != nil {
		return CollectionResult{}, fmt.Errorf("inspect image rootfs: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return CollectionResult{}, errors.New("image rootfs must be a regular file, not a symlink")
	}
	if err := hostpath.CheckTrustedFile("image_rootfs", rootfsPath); err != nil {
		return CollectionResult{}, fmt.Errorf("validate image rootfs trust: %w", err)
	}
	if err := verifyFileDigest(rootfsPath, expectedDigest); err != nil {
		return CollectionResult{}, err
	}
	if err := os.Remove(rootfsPath); err != nil {
		return CollectionResult{}, fmt.Errorf("delete image rootfs: %w", err)
	}
	if err := syncDirectory(imageRoot); err != nil {
		return CollectionResult{}, err
	}
	return CollectionResult{RootFSImage: rootfsPath, Changed: true}, nil
}

func validateRootFSPath(path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("rootfs path must be clean and absolute")
	}
	if filepath.Dir(path) != imageRoot {
		return "", fmt.Errorf("rootfs path must be directly beneath %s", imageRoot)
	}
	match := rootFSNamePattern.FindStringSubmatch(filepath.Base(path))
	if match == nil {
		return "", errors.New("rootfs filename must be sha256-<64 lowercase hexadecimal digits>.ext4")
	}
	return match[1], nil
}

func ensureRootFSUnreferenced(
	images map[string]config.ContainerImage,
	builds []microvm.AppBuild,
	machines []microvm.Sandbox,
	rootfsPath string,
) error {
	var publishedReferences []string
	for imageID, image := range images {
		if image.RootFSImage == rootfsPath {
			publishedReferences = append(publishedReferences, imageID)
		}
	}
	if len(publishedReferences) > 0 {
		sort.Strings(publishedReferences)
		return fmt.Errorf("%w: %s is used by %v", ErrRootFSInUse, rootfsPath, publishedReferences)
	}
	var unresolvedBuilds []string
	for _, build := range builds {
		if _, exists := images[build.BuildID]; !exists {
			unresolvedBuilds = append(unresolvedBuilds, build.AppID+"/"+build.BuildID)
		}
	}
	if len(unresolvedBuilds) > 0 {
		sort.Strings(unresolvedBuilds)
		return fmt.Errorf("%w: %v", ErrUnresolvedAppBuild, unresolvedBuilds)
	}

	var unresolved []string
	for _, machine := range machines {
		if machine.State == microvm.StateTerminated {
			continue
		}
		if _, exists := images[machine.BuildID]; !exists {
			unresolved = append(unresolved, machine.ID)
		}
	}
	if len(unresolved) > 0 {
		sort.Strings(unresolved)
		return fmt.Errorf("%w: %v", ErrUnresolvedActiveImage, unresolved)
	}
	return nil
}

func listRegisteredBuilds(store microvm.ResourceStore) ([]microvm.AppBuild, error) {
	return store.ListAllAppBuilds()
}

func verifyFileDigest(path, expected string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open image rootfs: %w", err)
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return fmt.Errorf("hash image rootfs: %w", err)
	}
	actual := fmt.Sprintf("%x", digest.Sum(nil))
	if actual != expected {
		return fmt.Errorf("image rootfs SHA-256 is %s, want filename digest %s", actual, expected)
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open image root directory: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync image root directory: %w", err)
	}
	return nil
}
