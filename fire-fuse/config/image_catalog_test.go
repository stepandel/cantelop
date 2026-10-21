package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadImageCatalog(t *testing.T) {
	image := validImage()
	id := validImageID()
	contents := fmt.Sprintf(`{
  "schema_version": 1,
  "images": {
    %q: {
      "rootfs_image": %q,
      "rootfs_sha256": %q,
      "rootfs_size_bytes": %d,
      "process": {
        "args": ["/bin/server"],
        "env": ["PATH=/usr/bin:/bin"],
        "working_directory": "/app",
        "uid": 1000,
        "gid": 1000
      }
    }
  }
}`, id, image.RootFSImage, image.RootFSSHA256, image.RootFSSizeBytes)
	path := writeCatalog(t, contents)

	images, err := LoadImageCatalog(path)
	if err != nil {
		t.Fatalf("LoadImageCatalog() error = %v", err)
	}
	if len(images) != 1 || images[id].RootFSImage != image.RootFSImage {
		t.Fatalf("LoadImageCatalog() = %#v", images)
	}
}

func TestLoadImageCatalogAllowsEmptyCatalog(t *testing.T) {
	images, err := LoadImageCatalog(writeCatalog(t, `{"schema_version":1,"images":{}}`))
	if err != nil {
		t.Fatalf("LoadImageCatalog() error = %v", err)
	}
	if len(images) != 0 {
		t.Fatalf("LoadImageCatalog() = %#v, want empty", images)
	}
}

func TestLoadImageCatalogRejectsInvalidDocuments(t *testing.T) {
	tests := []struct {
		name     string
		contents string
		message  string
	}{
		{name: "unknown field", contents: `{"schema_version":1,"images":{},"extra":true}`, message: "unknown field"},
		{name: "wrong schema", contents: `{"schema_version":2,"images":{}}`, message: "schema_version"},
		{name: "trailing document", contents: `{"schema_version":1,"images":{}} {}`, message: "exactly one"},
		{name: "invalid image", contents: `{"schema_version":1,"images":{"unsafe":{}}}`, message: "invalid image ID"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := LoadImageCatalog(writeCatalog(t, test.contents))
			if !errors.Is(err, ErrInvalid) && test.name != "unknown field" {
				t.Fatalf("LoadImageCatalog() error = %v, want ErrInvalid", err)
			}
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("LoadImageCatalog() error = %v, want %q", err, test.message)
			}
		})
	}
}

func writeCatalog(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "image-catalog.json")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}
