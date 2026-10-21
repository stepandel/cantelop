package imagecatalog

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/config"
	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
)

func TestRetireRejectsReferencedAppBuild(t *testing.T) {
	catalogPath := writeRetirementCatalog(t)
	_, err := retire(catalogPath, map[string]config.ContainerImage{testImageID: testImage()}, []microvm.AppBuild{{AppID: "app-test", BuildID: testImageID, CreatedAt: time.Now()}}, nil, testImageID)
	if !errors.Is(err, ErrBuildInUse) {
		t.Fatalf("retire() error = %v, want ErrBuildInUse", err)
	}
}

func TestRetireRejectsRunningSandboxAndAllowsTerminated(t *testing.T) {
	for _, state := range []microvm.State{microvm.StateStarting, microvm.StateRunning, microvm.StateFailed} {
		t.Run(string(state), func(t *testing.T) {
			catalogPath := writeRetirementCatalog(t)
			_, err := retire(catalogPath, map[string]config.ContainerImage{testImageID: testImage()}, nil, []microvm.Sandbox{{ID: "sandbox-test", BuildID: testImageID, State: state}}, testImageID)
			if !errors.Is(err, ErrBuildInUse) {
				t.Fatalf("retire() error = %v", err)
			}
		})
	}
	catalogPath := writeRetirementCatalog(t)
	result, err := retire(catalogPath, map[string]config.ContainerImage{testImageID: testImage()}, nil, []microvm.Sandbox{{ID: "sandbox-test", BuildID: testImageID, State: microvm.StateTerminated}}, testImageID)
	if err != nil || !result.Changed || result.BuildID != testImageID {
		t.Fatalf("retire() = %#v, %v", result, err)
	}
}

func TestRetireIsIdempotent(t *testing.T) {
	result, err := retire("/unused", map[string]config.ContainerImage{}, nil, nil, testImageID)
	if err != nil || result.Changed || result.BuildID != testImageID {
		t.Fatalf("retire() = %#v, %v", result, err)
	}
}

func writeRetirementCatalog(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "image-catalog.json")
	if err := writeAtomic(path, config.ImageCatalogDocument{SchemaVersion: config.ImageCatalogSchemaVersion, Images: map[string]config.ContainerImage{testImageID: testImage()}}); err != nil {
		t.Fatal(err)
	}
	return path
}
