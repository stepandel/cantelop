package imagecatalog

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/config"
	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
)

type scriptedReferences struct {
	calls     int
	responses func(call int) microvm.ImageReferences
}

func (s *scriptedReferences) ImageReferences() (microvm.ImageReferences, error) {
	s.calls++
	return s.responses(s.calls), nil
}

type collectorFixture struct {
	t          *testing.T
	collector  *Collector
	references *scriptedReferences
	directory  string
	catalog    string
	now        time.Time
}

func newCollectorFixture(t *testing.T, images map[string]config.ContainerImage) *collectorFixture {
	t.Helper()
	root := t.TempDir()
	f := &collectorFixture{t: t, directory: filepath.Join(root, "images"), catalog: filepath.Join(root, "catalog.json"),
		now: time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)}
	if err := os.Mkdir(f.directory, 0o700); err != nil {
		t.Fatal(err)
	}
	f.references = &scriptedReferences{responses: func(int) microvm.ImageReferences {
		return microvm.ImageReferences{BuildIDs: map[string]struct{}{}}
	}}
	collector, err := NewCollector(f.catalog, f.directory, 2*time.Hour, f.references)
	if err != nil {
		t.Fatal(err)
	}
	collector.now = func() time.Time { return f.now }
	collector.lock = func() (io.Closer, error) { return io.NopCloser(nil), nil }
	collector.load = config.LoadImageCatalog
	f.collector = collector
	f.writeCatalog(images)
	return f
}

// image places a digest-named rootfs in the fixture's image directory.
func (f *collectorFixture) image(digestChar, command string) (string, config.ContainerImage) {
	f.t.Helper()
	image := testImage()
	image.RootFSSHA256 = strings.Repeat(digestChar, 64)
	image.RootFSImage = filepath.Join(f.directory, "sha256-"+image.RootFSSHA256+".ext4")
	image.Process.Args = []string{command}
	if err := os.WriteFile(image.RootFSImage, make([]byte, image.RootFSSizeBytes), 0o600); err != nil {
		f.t.Fatal(err)
	}
	id, err := config.ContainerImageID(image)
	if err != nil {
		f.t.Fatal(err)
	}
	return id, image
}

func (f *collectorFixture) writeCatalog(images map[string]config.ContainerImage) {
	f.t.Helper()
	if images == nil {
		images = map[string]config.ContainerImage{}
	}
	if err := writeAtomic(f.catalog, catalogDocument(images)); err != nil {
		f.t.Fatal(err)
	}
}

func (f *collectorFixture) collectAt(offset time.Duration) CollectionReport {
	f.t.Helper()
	f.now = time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC).Add(offset)
	report, err := f.collector.Collect()
	if err != nil {
		f.t.Fatalf("Collect() at %s: %v", offset, err)
	}
	return report
}

func (f *collectorFixture) published() map[string]config.ContainerImage {
	f.t.Helper()
	images, err := config.LoadImageCatalog(f.catalog)
	if err != nil {
		f.t.Fatal(err)
	}
	return images
}

func exists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Lstat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return err == nil
}

func pinned(ids ...string) func(int) microvm.ImageReferences {
	return func(int) microvm.ImageReferences {
		references := microvm.ImageReferences{BuildIDs: map[string]struct{}{}}
		for _, id := range ids {
			references.BuildIDs[id] = struct{}{}
		}
		return references
	}
}

func TestCollectorRetiresUnreferencedBuildOnlyAfterGrace(t *testing.T) {
	f := newCollectorFixture(t, nil)
	unusedID, unused := f.image("a", "/bin/unused")
	usedID, used := f.image("b", "/bin/used")
	f.writeCatalog(map[string]config.ContainerImage{unusedID: unused, usedID: used})
	f.references.responses = pinned(usedID)

	for _, offset := range []time.Duration{0, 2*time.Hour - time.Minute} {
		if report := f.collectAt(offset); len(report.RetiredBuilds) != 0 || len(report.RemovedFiles) != 0 {
			t.Fatalf("collected before grace at %s: %+v", offset, report)
		}
	}
	report := f.collectAt(2 * time.Hour)
	if !slices.Equal(report.RetiredBuilds, []string{unusedID}) || !slices.Equal(report.RemovedFiles, []string{unused.RootFSImage}) ||
		report.FreedBytes != unused.RootFSSizeBytes {
		t.Fatalf("report = %+v", report)
	}
	if images := f.published(); len(images) != 1 || images[usedID].RootFSImage != used.RootFSImage {
		t.Fatalf("catalog = %v", images)
	}
	if exists(t, unused.RootFSImage) || !exists(t, used.RootFSImage) {
		t.Fatal("wrong rootfs files removed")
	}
}

func TestCollectorRestartsGraceWhenBuildIsReferencedAgain(t *testing.T) {
	f := newCollectorFixture(t, nil)
	id, image := f.image("a", "/bin/server")
	f.writeCatalog(map[string]config.ContainerImage{id: image})
	f.collectAt(0)
	f.references.responses = pinned(id)
	f.collectAt(time.Hour)
	f.references.responses = pinned()
	if report := f.collectAt(2 * time.Hour); len(report.RetiredBuilds) != 0 {
		t.Fatalf("grace did not restart: %+v", report)
	}
	if report := f.collectAt(4 * time.Hour); len(report.RetiredBuilds) != 1 {
		t.Fatalf("not collected after a full grace: %+v", report)
	}
}

func TestCollectorWaitsForPendingUploads(t *testing.T) {
	f := newCollectorFixture(t, nil)
	id, image := f.image("a", "/bin/server")
	f.writeCatalog(map[string]config.ContainerImage{id: image})
	f.collectAt(0)
	f.references.responses = func(int) microvm.ImageReferences {
		return microvm.ImageReferences{BuildIDs: map[string]struct{}{}, PendingUploads: 1}
	}
	if report := f.collectAt(3 * time.Hour); report.Skipped == "" || len(report.RetiredBuilds) != 0 {
		t.Fatalf("collected during a pending upload: %+v", report)
	}
	if len(f.published()) != 1 || !exists(t, image.RootFSImage) {
		t.Fatal("pending upload pass changed the host")
	}
}

func TestCollectorRestoresBuildReferencedDuringRewrite(t *testing.T) {
	f := newCollectorFixture(t, nil)
	id, image := f.image("a", "/bin/server")
	f.writeCatalog(map[string]config.ContainerImage{id: image})
	f.collectAt(0)
	// The pass reads references once before and once after the catalog rewrite.
	f.references.calls = 0
	f.references.responses = func(call int) microvm.ImageReferences {
		if call == 2 {
			return pinned(id)(call)
		}
		return pinned()(call)
	}
	if report := f.collectAt(2 * time.Hour); len(report.RetiredBuilds) != 0 || len(report.RemovedFiles) != 0 {
		t.Fatalf("report = %+v", report)
	}
	if images := f.published(); len(images) != 1 || !exists(t, image.RootFSImage) {
		t.Fatalf("referenced Build was not restored: %v", images)
	}
	f.references.responses = pinned()
	if report := f.collectAt(3 * time.Hour); len(report.RetiredBuilds) != 0 {
		t.Fatalf("restored Build kept its old grace: %+v", report)
	}
}

func TestCollectorKeepsRootFSSharedWithAnotherBuild(t *testing.T) {
	f := newCollectorFixture(t, nil)
	unusedID, unused := f.image("a", "/bin/old")
	shared := unused
	shared.Process.Args = []string{"/bin/new"}
	sharedID, err := config.ContainerImageID(shared)
	if err != nil {
		t.Fatal(err)
	}
	f.writeCatalog(map[string]config.ContainerImage{unusedID: unused, sharedID: shared})
	f.references.responses = pinned(sharedID)
	f.collectAt(0)
	report := f.collectAt(2 * time.Hour)
	if !slices.Equal(report.RetiredBuilds, []string{unusedID}) || len(report.RemovedFiles) != 0 {
		t.Fatalf("report = %+v", report)
	}
	if !exists(t, unused.RootFSImage) {
		t.Fatal("shared rootfs removed")
	}
}

func TestCollectorIgnoresImagesOutsideTheImageDirectory(t *testing.T) {
	f := newCollectorFixture(t, nil)
	image := testImage()
	id, err := config.ContainerImageID(image)
	if err != nil {
		t.Fatal(err)
	}
	f.writeCatalog(map[string]config.ContainerImage{id: image})
	f.collectAt(0)
	if report := f.collectAt(10 * time.Hour); len(report.RetiredBuilds) != 0 {
		t.Fatalf("collected an image outside the image directory: %+v", report)
	}
}

func TestCollectorSweepsOrphanRootFSAfterGrace(t *testing.T) {
	f := newCollectorFixture(t, nil)
	_, orphan := f.image("c", "/bin/orphan")
	kept := []string{"ubuntu-24.ext4", ".prepared-1.ext4", "sha256-short.ext4"}
	for _, name := range kept {
		if err := os.WriteFile(filepath.Join(f.directory, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f.collectAt(0)
	report := f.collectAt(2 * time.Hour)
	if !slices.Equal(report.RemovedFiles, []string{orphan.RootFSImage}) || len(report.RetiredBuilds) != 0 {
		t.Fatalf("report = %+v", report)
	}
	for _, name := range kept {
		if !exists(t, filepath.Join(f.directory, name)) {
			t.Fatalf("%s removed", name)
		}
	}
}

func TestCollectorSkipsOrphanSweepWhenInUseBuildIsUnpublished(t *testing.T) {
	f := newCollectorFixture(t, nil)
	_, orphan := f.image("c", "/bin/orphan")
	f.references.responses = pinned("sha256:" + strings.Repeat("d", 64))
	f.collectAt(0)
	report := f.collectAt(5 * time.Hour)
	if report.OrphanSweepSkipped == "" || len(report.RemovedFiles) != 0 || !exists(t, orphan.RootFSImage) {
		t.Fatalf("orphan removed while a live reference is unresolved: %+v", report)
	}
}

func TestCollectorDefersDuringRuntimeDeployment(t *testing.T) {
	f := newCollectorFixture(t, nil)
	f.collector.lock = func() (io.Closer, error) { return nil, ErrCatalogDeployment }
	if report := f.collectAt(0); report.Skipped == "" {
		t.Fatalf("report = %+v", report)
	}
}

func TestNewCollectorRequiresMinimumGrace(t *testing.T) {
	if _, err := NewCollector("/catalog.json", "/images", MinCollectionGrace-time.Second, &scriptedReferences{}); err == nil {
		t.Fatal("short grace accepted")
	}
	if _, err := NewCollector("/catalog.json", "images", MinCollectionGrace, &scriptedReferences{}); err == nil {
		t.Fatal("relative image directory accepted")
	}
}
