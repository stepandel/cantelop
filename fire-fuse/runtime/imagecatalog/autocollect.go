package imagecatalog

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/config"
	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
)

// MinCollectionGrace bounds how quickly the collector may act on a Build that
// stopped being referenced. It must outlast the gap between an operator
// publishing a Build and attaching it, and between an install linking a rootfs
// and publishing it.
const MinCollectionGrace = time.Hour

// ImageReferenceSource reports what keeps published Builds in use.
type ImageReferenceSource interface {
	ImageReferences() (microvm.ImageReferences, error)
}

// Collector retires unreferenced customer Builds from the catalog and deletes
// their rootfs files while fire-fuse runs. A Build or rootfs file is collected
// only after every pass for the whole grace period found it unreferenced. Grace
// timers live in memory, so a restart starts them again.
type Collector struct {
	catalogPath    string
	imageDirectory string
	grace          time.Duration
	references     ImageReferenceSource
	now            func() time.Time
	lock           func() (io.Closer, error)
	load           func(string) (map[string]config.ContainerImage, error)
	// unreferencedSince is keyed by "build:<id>" and "file:<path>".
	unreferencedSince map[string]time.Time
}

// CollectionReport describes one collection pass.
type CollectionReport struct {
	RetiredBuilds []string
	RemovedFiles  []string
	FreedBytes    int64
	// Skipped explains a pass that changed nothing for safety.
	Skipped string
	// OrphanSweepSkipped explains why unpublished rootfs files were not examined.
	OrphanSweepSkipped string
}

func NewCollector(catalogPath, imageDirectory string, grace time.Duration, references ImageReferenceSource) (*Collector, error) {
	if !filepath.IsAbs(catalogPath) || !filepath.IsAbs(imageDirectory) || filepath.Clean(imageDirectory) != imageDirectory {
		return nil, errors.New("image collection requires absolute catalog and image directory paths")
	}
	if grace < MinCollectionGrace {
		return nil, fmt.Errorf("image collection grace must be at least %s", MinCollectionGrace)
	}
	if references == nil {
		return nil, errors.New("image collection requires a Build reference source")
	}
	return &Collector{
		catalogPath:       catalogPath,
		imageDirectory:    imageDirectory,
		grace:             grace,
		references:        references,
		now:               time.Now,
		lock:              func() (io.Closer, error) { return acquireLock(catalogPath + ".lock") },
		load:              config.LoadTrustedImageCatalog,
		unreferencedSince: make(map[string]time.Time),
	}, nil
}

// Run collects once immediately and then on every interval until ctx ends.
func (c *Collector) Run(ctx context.Context, interval time.Duration, observe func(CollectionReport, error)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		report, err := c.Collect()
		observe(report, err)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Collect runs one pass under the catalog writer lock. It never acts while a
// Build upload is pending, because an install publishes its Build before the
// same upload attaches it to the App.
func (c *Collector) Collect() (report CollectionReport, err error) {
	lock, err := c.lock()
	if errors.Is(err, ErrCatalogDeployment) {
		return CollectionReport{Skipped: "runtime deployment holds the image catalog"}, nil
	}
	if err != nil {
		return CollectionReport{}, err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()

	images, err := c.load(c.catalogPath)
	if err != nil {
		return CollectionReport{}, fmt.Errorf("load current image catalog: %w", err)
	}
	references, err := c.references.ImageReferences()
	if err != nil {
		return CollectionReport{}, fmt.Errorf("read Build references: %w", err)
	}
	if references.PendingUploads > 0 {
		return CollectionReport{Skipped: "a Build upload is pending"}, nil
	}

	now := c.now()
	observed := make(map[string]struct{})
	var candidates []string
	for buildID, image := range images {
		if _, used := references.BuildIDs[buildID]; used || !c.collectableRootFS(image.RootFSImage, image.RootFSSHA256) {
			continue
		}
		if c.unreferencedFor("build:"+buildID, now, observed) {
			candidates = append(candidates, buildID)
		}
	}
	orphans, skipped, err := c.findOrphans(images, references, now, observed)
	if err != nil {
		return CollectionReport{}, err
	}
	report.OrphanSweepSkipped = skipped
	for key := range c.unreferencedSince {
		if _, ok := observed[key]; !ok {
			delete(c.unreferencedSince, key)
		}
	}
	slices.Sort(candidates)

	removals := orphans
	if len(candidates) > 0 {
		retired, err := c.retireFromCatalog(images, candidates)
		if err != nil {
			return CollectionReport{}, err
		}
		report.RetiredBuilds = retired
		for _, buildID := range candidates {
			// Retired or found in use again: either way its grace starts over.
			delete(c.unreferencedSince, "build:"+buildID)
		}
		remaining := make(map[string]struct{}, len(images))
		for buildID, image := range images {
			if !slices.Contains(retired, buildID) {
				remaining[image.RootFSImage] = struct{}{}
			}
		}
		for _, buildID := range retired {
			path := images[buildID].RootFSImage
			if _, shared := remaining[path]; !shared && !slices.Contains(removals, path) {
				removals = append(removals, path)
			}
		}
	}

	var removeErr error
	for _, path := range removals {
		size, removed, err := removeRootFS(path)
		if err != nil {
			removeErr = errors.Join(removeErr, err)
			continue
		}
		delete(c.unreferencedSince, "file:"+path)
		if removed {
			report.RemovedFiles = append(report.RemovedFiles, path)
			report.FreedBytes += size
		}
	}
	if len(report.RemovedFiles) > 0 {
		removeErr = errors.Join(removeErr, syncDirectory(c.imageDirectory))
	}
	return report, removeErr
}

// retireFromCatalog removes candidates, then re-reads references. A Build that
// an App attached or a Sandbox started using during the rewrite is published
// again before its rootfs could be deleted. Sandbox records are saved before
// their image is resolved, so a launch that saw the old catalog is visible here.
func (c *Collector) retireFromCatalog(images map[string]config.ContainerImage, candidates []string) ([]string, error) {
	next := make(map[string]config.ContainerImage, len(images))
	for buildID, image := range images {
		if !slices.Contains(candidates, buildID) {
			next[buildID] = image
		}
	}
	if err := writeAtomic(c.catalogPath, catalogDocument(next)); err != nil {
		return nil, err
	}
	references, err := c.references.ImageReferences()
	if err != nil {
		restoreErr := writeAtomic(c.catalogPath, catalogDocument(images))
		return nil, errors.Join(fmt.Errorf("recheck Build references: %w", err), restoreErr)
	}
	var retired, restored []string
	for _, buildID := range candidates {
		if _, used := references.BuildIDs[buildID]; used || references.PendingUploads > 0 {
			next[buildID] = images[buildID]
			restored = append(restored, buildID)
			continue
		}
		retired = append(retired, buildID)
	}
	if len(restored) > 0 {
		if err := writeAtomic(c.catalogPath, catalogDocument(next)); err != nil {
			return nil, fmt.Errorf("restore Builds referenced during collection: %w", err)
		}
	}
	return retired, nil
}

// findOrphans lists digest-named rootfs files that no catalog entry references.
// They come from manual retirement without collection or from a crash between
// a catalog rewrite and the file deletion. Without a catalog entry the file
// cannot be mapped to a Build, so the sweep runs only when every in-use Build
// resolves through the catalog.
func (c *Collector) findOrphans(
	images map[string]config.ContainerImage,
	references microvm.ImageReferences,
	now time.Time,
	observed map[string]struct{},
) ([]string, string, error) {
	for buildID := range references.BuildIDs {
		if _, published := images[buildID]; !published {
			return nil, "an in-use Build is missing from the image catalog: " + buildID, nil
		}
	}
	published := make(map[string]struct{}, len(images))
	for _, image := range images {
		published[image.RootFSImage] = struct{}{}
	}
	entries, err := os.ReadDir(c.imageDirectory)
	if err != nil {
		return nil, "", fmt.Errorf("list image directory: %w", err)
	}
	var orphans []string
	for _, entry := range entries {
		path := filepath.Join(c.imageDirectory, entry.Name())
		if !rootFSNamePattern.MatchString(entry.Name()) || !entry.Type().IsRegular() {
			continue
		}
		if _, ok := published[path]; ok {
			continue
		}
		if c.unreferencedFor("file:"+path, now, observed) {
			orphans = append(orphans, path)
		}
	}
	return orphans, "", nil
}

// collectableRootFS limits automatic collection to digest-named customer
// rootfs files directly in the image directory. Platform kernels and profile
// rootfs images never match.
func (c *Collector) collectableRootFS(path, digest string) bool {
	if filepath.Dir(path) != c.imageDirectory {
		return false
	}
	match := rootFSNamePattern.FindStringSubmatch(filepath.Base(path))
	return match != nil && match[1] == digest
}

func (c *Collector) unreferencedFor(key string, now time.Time, observed map[string]struct{}) bool {
	observed[key] = struct{}{}
	since, ok := c.unreferencedSince[key]
	if !ok {
		c.unreferencedSince[key] = now
		return false
	}
	return now.Sub(since) >= c.grace
}

// removeRootFS deletes one regular file. Running VMs keep their bind-mounted
// inode, so deletion cannot disturb them.
func removeRootFS(path string) (int64, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("inspect image rootfs: %w", err)
	}
	if !info.Mode().IsRegular() {
		return 0, false, fmt.Errorf("image rootfs %s is not a regular file", path)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, false, fmt.Errorf("delete image rootfs: %w", err)
	}
	return info.Size(), true, nil
}

func catalogDocument(images map[string]config.ContainerImage) config.ImageCatalogDocument {
	return config.ImageCatalogDocument{SchemaVersion: config.ImageCatalogSchemaVersion, Images: images}
}
