package imagecatalog

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stepandel/cantelop/fire-fuse/config"
)

func TestPublicationRejectsRootFSRemovedWhileWaitingForCatalogLock(t *testing.T) {
	for _, published := range []bool{true, false} {
		name := "orphan_rootfs"
		if published {
			name = "retired_build"
		}
		t.Run(name, func(t *testing.T) {
			f := newCollectorFixture(t, nil)
			data := make([]byte, 1024)
			image := testImage()
			image.RootFSSHA256 = fmt.Sprintf("%x", sha256.Sum256(data))
			image.RootFSImage = filepath.Join(f.directory, "sha256-"+image.RootFSSHA256+".ext4")
			if err := os.WriteFile(image.RootFSImage, data, 0o600); err != nil {
				t.Fatal(err)
			}
			id, err := config.ContainerImageID(image)
			if err != nil {
				t.Fatal(err)
			}
			if published {
				f.writeCatalog(map[string]config.ContainerImage{id: image})
			}
			f.collectAt(0)
			f.now = f.now.Add(2 * time.Hour)

			collectorLocked := make(chan struct{})
			resumeCollection := make(chan struct{})
			var releaseOnce sync.Once
			releaseCollector := func() { releaseOnce.Do(func() { close(resumeCollection) }) }
			defer releaseCollector()
			f.collector.lock = func() (io.Closer, error) {
				file, err := acquirePublicationTestLock(f.catalog + ".lock")
				if err != nil {
					return nil, err
				}
				close(collectorLocked)
				<-resumeCollection
				return file, nil
			}
			type collectionResult struct {
				report CollectionReport
				err    error
			}
			collection := make(chan collectionResult, 1)
			go func() {
				report, err := f.collector.Collect()
				collection <- collectionResult{report, err}
			}()
			select {
			case <-collectorLocked:
			case result := <-collection:
				t.Fatalf("collection stopped before acquiring its lock: %v", result.err)
			case <-time.After(5 * time.Second):
				t.Fatal("collection did not acquire the catalog lock")
			}

			// Publish a new process contract using the old rootfs. Both public
			// publishing entry points use this production lock/validation path;
			// host trust checks use this non-root fixture while hashing stays real.
			image.Process.Args = []string{"/bin/new-server"}
			publicationWaiting := make(chan struct{})
			publication := make(chan error, 1)
			go func() {
				_, err := publishInstalledImage(f.catalog, image,
					func(path string) (*os.File, error) {
						close(publicationWaiting)
						return acquirePublicationTestLock(path)
					},
					func(image config.ContainerImage) error {
						return verifyFileDigest(image.RootFSImage, image.RootFSSHA256)
					},
					config.LoadImageCatalog)
				publication <- err
			}()
			select {
			case <-publicationWaiting:
			case err := <-publication:
				t.Fatalf("publication stopped before waiting for its lock: %v", err)
			case <-time.After(5 * time.Second):
				t.Fatal("publication did not attempt the catalog lock")
			}
			releaseCollector()
			result := <-collection
			if result.err != nil || len(result.report.RemovedFiles) != 1 {
				t.Fatalf("collection = %+v, error = %v", result.report, result.err)
			}
			if err := <-publication; !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("publication error = %v, want missing rootfs", err)
			}
			if images := f.published(); len(images) != 0 || exists(t, image.RootFSImage) {
				t.Fatalf("publication recreated a dangling catalog entry: %v", images)
			}
		})
	}
}

// Use the same flock boundary as acquireLock without requiring root ownership.
func acquirePublicationTestLock(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return file, nil
}
