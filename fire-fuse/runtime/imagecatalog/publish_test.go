package imagecatalog

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stepandel/cantelop/fire-fuse/config"
	"github.com/stepandel/cantelop/fire-fuse/guest-runtime/guestconfig"
)

const testImageID = "sha256:e4c0bb5ba48f30f6cf4b9da62b990ef532a6644bda683af8243c54035331b4f8"

func TestPublishAddsImageMonotonicallyAndIsIdempotent(t *testing.T) {
	catalogPath := filepath.Join(t.TempDir(), "image-catalog.json")
	if err := writeAtomic(catalogPath, config.ImageCatalogDocument{
		SchemaVersion: config.ImageCatalogSchemaVersion,
		Images:        map[string]config.ContainerImage{},
	}); err != nil {
		t.Fatalf("write initial catalog: %v", err)
	}
	image := testImage()
	result, err := publish(catalogPath, map[string]config.ContainerImage{}, image)
	if err != nil {
		t.Fatalf("publish() error = %v", err)
	}
	if result.BuildID != testImageID || !result.Changed {
		t.Fatalf("publish() result = %#v, want build %s", result, testImageID)
	}
	images, err := config.LoadImageCatalog(catalogPath)
	if err != nil {
		t.Fatalf("LoadImageCatalog() error = %v", err)
	}
	if _, exists := images[testImageID]; !exists {
		t.Fatalf("published catalog does not contain %s", testImageID)
	}

	result, err = publish(catalogPath, images, image)
	if err != nil {
		t.Fatalf("idempotent publish() error = %v", err)
	}
	if result.BuildID != testImageID || result.Changed {
		t.Fatalf("idempotent publish() result = %#v, want unchanged %s", result, testImageID)
	}

	nextImage := image
	nextImage.Process.Args = []string{"/bin/server-v2"}
	nextBuildID, err := config.ContainerImageID(nextImage)
	if err != nil {
		t.Fatal(err)
	}
	result, err = publish(catalogPath, images, nextImage)
	if err != nil {
		t.Fatalf("publish next build: %v", err)
	}
	if result.BuildID != nextBuildID || !result.Changed {
		t.Fatalf("next publish = %#v", result)
	}
}

func TestPublishRejectsConflictingMetadataForExistingID(t *testing.T) {
	image := testImage()
	conflicting := image
	conflicting.RootFSImage = "/var/lib/fire-fuse/images/other.ext4"

	_, err := publish("/unused", map[string]config.ContainerImage{testImageID: conflicting}, image)
	if err == nil || !strings.Contains(err.Error(), "different catalog metadata") {
		t.Fatalf("publish() error = %v, want metadata conflict", err)
	}
}

func TestWriteAtomicUsesTrustedCatalogMode(t *testing.T) {
	catalogPath := filepath.Join(t.TempDir(), "image-catalog.json")
	if err := writeAtomic(catalogPath, config.ImageCatalogDocument{
		SchemaVersion: config.ImageCatalogSchemaVersion,
		Images:        map[string]config.ContainerImage{},
	}); err != nil {
		t.Fatalf("writeAtomic() error = %v", err)
	}
	info, err := os.Stat(catalogPath)
	if err != nil {
		t.Fatalf("stat catalog: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o640 {
		t.Fatalf("catalog mode = %04o, want 0640", got)
	}
	if _, err := config.LoadImageCatalog(catalogPath); err != nil {
		t.Fatalf("LoadImageCatalog() error = %v", err)
	}
}

func TestLoadManifestIsStrict(t *testing.T) {
	valid := `{"schema_version":1,"image":{"rootfs_image":"/var/lib/fire-fuse/images/sha256-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.ext4","rootfs_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","rootfs_size_bytes":1024,"process":{"args":["/bin/server"],"env":["PATH=/usr/bin:/bin"],"working_directory":"/app","uid":1000,"gid":1000}}}`
	tests := []struct {
		name    string
		content string
		wantErr bool
	}{
		{name: "valid", content: valid},
		{name: "unknown field", content: strings.Replace(valid, `"schema_version":1`, `"schema_version":1,"extra":true`, 1), wantErr: true},
		{name: "trailing object", content: valid + `{}`, wantErr: true},
		{name: "wrong schema", content: strings.Replace(valid, `"schema_version":1`, `"schema_version":2`, 1), wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "manifest.json")
			if err := os.WriteFile(path, []byte(test.content), 0o600); err != nil {
				t.Fatalf("write manifest: %v", err)
			}
			_, err := LoadManifest(path)
			if (err != nil) != test.wantErr {
				t.Fatalf("LoadManifest() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func TestPublishRejectsInvalidImageBeforeWriting(t *testing.T) {
	image := testImage()
	image.RootFSSHA256 = "not-a-digest"
	_, err := publish("/unused", nil, image)
	if err == nil {
		t.Fatal("publish() succeeded with an invalid image")
	}
	if !errors.Is(err, config.ErrInvalid) && !strings.Contains(err.Error(), "rootfs_sha256") {
		t.Fatalf("publish() error = %v, want invalid rootfs digest", err)
	}
}

func TestCheckDeploymentLease(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "image-catalog.json.lock")
	if err := checkDeploymentLease(lockPath); err != nil {
		t.Fatalf("checkDeploymentLease() without lease error = %v", err)
	}
	leasePath := lockPath + ".deployment"
	if err := os.WriteFile(leasePath, []byte("deployment"), 0o600); err != nil {
		t.Fatalf("write deployment lease: %v", err)
	}
	if err := checkDeploymentLease(lockPath); !errors.Is(err, ErrCatalogDeployment) {
		t.Fatalf("checkDeploymentLease() error = %v, want ErrCatalogDeployment", err)
	}
}

func testImage() config.ContainerImage {
	return config.ContainerImage{
		RootFSImage:     "/var/lib/fire-fuse/images/sha256-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.ext4",
		RootFSSHA256:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		RootFSSizeBytes: 1024,
		Process: guestconfig.Process{
			Args:             []string{"/bin/server"},
			Env:              []string{"PATH=/usr/bin:/bin"},
			WorkingDirectory: "/app",
			UID:              1000,
			GID:              1000,
		},
	}
}
