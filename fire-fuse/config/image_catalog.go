package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/stepandel/cantelop/fire-fuse/host/hostpath"
)

// ImageCatalogSchemaVersion is the on-disk image catalog schema written by
// trusted host-side publishers.
const ImageCatalogSchemaVersion = 1

// ImageCatalogDocument is the independently published, versioned set of
// immutable workload images available to new VM requests.
type ImageCatalogDocument struct {
	SchemaVersion int                       `json:"schema_version"`
	Images        map[string]ContainerImage `json:"images"`
}

// LoadImageCatalog strictly decodes and validates one image catalog document.
// Callers that use the result as host configuration must establish file trust
// before calling it.
func LoadImageCatalog(path string) (map[string]ContainerImage, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open image catalog: %w", err)
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var document ImageCatalogDocument
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode image catalog: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: image catalog must contain exactly one JSON object", ErrInvalid)
	}
	if document.SchemaVersion != ImageCatalogSchemaVersion {
		return nil, fmt.Errorf(
			"%w: image catalog schema_version is %d, want %d",
			ErrInvalid,
			document.SchemaVersion,
			ImageCatalogSchemaVersion,
		)
	}
	if document.Images == nil {
		document.Images = map[string]ContainerImage{}
	}
	if err := validateContainerImages(document.Images); err != nil {
		return nil, err
	}
	return document.Images, nil
}

// LoadTrustedImageCatalog validates the host ownership boundary before reading
// the current catalog snapshot.
func LoadTrustedImageCatalog(path string) (map[string]ContainerImage, error) {
	if err := hostpath.CheckTrustedFile("image_catalog", path); err != nil {
		return nil, fmt.Errorf("validate image catalog trust: %w", err)
	}
	return LoadImageCatalog(path)
}

// ResolveTrustedImage reloads the trusted catalog and resolves one immutable
// image for a new VM request. Atomic catalog replacement therefore becomes
// visible without restarting fire-fuse.
func ResolveTrustedImage(path, imageID string) (ContainerImage, error) {
	images, err := LoadTrustedImageCatalog(path)
	if err != nil {
		return ContainerImage{}, err
	}
	image, exists := images[imageID]
	if !exists {
		return ContainerImage{}, fmt.Errorf("%w: %s", ErrImageNotFound, imageID)
	}
	return image, nil
}

func validateContainerImages(images map[string]ContainerImage) error {
	for id, image := range images {
		if !imageIDPattern.MatchString(id) {
			return fmt.Errorf("%w: invalid image ID %q", ErrInvalid, id)
		}
		if err := validateAbsolutePath("images."+id+".rootfs_image", image.RootFSImage); err != nil {
			return err
		}
		computedID, err := ContainerImageID(image)
		if err != nil {
			return fmt.Errorf("%w: images.%s: %v", ErrInvalid, id, err)
		}
		if computedID != id {
			return fmt.Errorf("%w: images.%s content identity is %s", ErrInvalid, id, computedID)
		}
	}
	return nil
}
