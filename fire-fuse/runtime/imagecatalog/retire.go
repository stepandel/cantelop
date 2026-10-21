package imagecatalog

import (
	"errors"
	"fmt"
	"os"
	"sort"

	"github.com/stepandel/cantelop/fire-fuse/config"
	"github.com/stepandel/cantelop/fire-fuse/host/hostpath"
	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
)

var ErrBuildInUse = errors.New("image build is referenced by non-terminated sandboxes")

// RetirementResult describes an idempotent catalog retirement. RootFSImage is
// retained in the result so a later, separately guarded garbage collector can
// identify the now-unpublished artifact.
type RetirementResult struct {
	BuildID     string
	RootFSImage string
	Changed     bool
}

// Retire removes a Build only when no App or non-terminated Sandbox references it.
func Retire(catalogPath, statePath, buildID string) (result RetirementResult, err error) {
	if os.Geteuid() != 0 {
		return RetirementResult{}, errors.New("image retirement requires root")
	}
	if err := microvm.ValidateBuildID(buildID); err != nil {
		return RetirementResult{}, fmt.Errorf("validate build ID: %w", err)
	}
	if err := hostpath.CheckTrustedFile("state_database", statePath); err != nil {
		return RetirementResult{}, fmt.Errorf("validate lifecycle state trust: %w", err)
	}
	store, err := microvm.OpenSQLiteStore(statePath)
	if err != nil {
		return RetirementResult{}, fmt.Errorf("acquire exclusive lifecycle state: %w", err)
	}
	defer func() {
		err = errors.Join(err, store.Close())
	}()

	lock, err := acquireLock(catalogPath + ".lock")
	if err != nil {
		return RetirementResult{}, err
	}
	defer func() {
		err = errors.Join(err, lock.Close())
	}()

	images, err := config.LoadTrustedImageCatalog(catalogPath)
	if err != nil {
		return RetirementResult{}, fmt.Errorf("load current image catalog: %w", err)
	}
	machines, err := store.ListActive()
	if err != nil {
		return RetirementResult{}, fmt.Errorf("list persisted microVMs: %w", err)
	}
	builds, err := store.ListAllAppBuilds()
	if err != nil {
		return RetirementResult{}, fmt.Errorf("list App Builds: %w", err)
	}
	return retire(catalogPath, images, builds, machines, buildID)
}

func retire(
	catalogPath string,
	images map[string]config.ContainerImage,
	builds []microvm.AppBuild,
	machines []microvm.Sandbox,
	buildID string,
) (RetirementResult, error) {
	physicalImage, published := images[buildID]
	if !published {
		return RetirementResult{BuildID: buildID}, nil
	}

	var references []string
	for _, build := range builds {
		if build.BuildID == buildID {
			references = append(references, "app:"+build.AppID)
		}
	}
	for _, machine := range machines {
		if machine.BuildID == buildID && machine.State != microvm.StateTerminated {
			references = append(references, machine.ID)
		}
	}
	if len(references) > 0 {
		sort.Strings(references)
		return RetirementResult{}, fmt.Errorf(
			"%w: %s is used by %v",
			ErrBuildInUse,
			buildID,
			references,
		)
	}

	next := make(map[string]config.ContainerImage, len(images)-1)
	for existingID, existing := range images {
		if existingID != buildID {
			next[existingID] = existing
		}
	}
	if err := writeAtomic(catalogPath, config.ImageCatalogDocument{
		SchemaVersion: config.ImageCatalogSchemaVersion,
		Images:        next,
	}); err != nil {
		return RetirementResult{}, err
	}
	result := RetirementResult{
		BuildID: buildID,
		Changed: true,
	}
	result.RootFSImage = physicalImage.RootFSImage
	return result, nil
}
