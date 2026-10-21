package microvm

import (
	"fmt"
	"time"
)

func (s *SQLiteStore) BeginAppBuildUpload(tenantID string, operation AppBuildOperation) error {
	return s.buildOperations.Begin(tenantID, operation)
}

func (s *SQLiteStore) GetAppBuildOperationForComputeTenant(tenantID, appID, operationID string) (AppBuildOperation, error) {
	return s.buildOperations.Get(tenantID, appID, operationID)
}

func (s *SQLiteStore) CompletePreparedAppBuild(operationID, buildID string, at time.Time) (AppBuildOperation, AppBuild, error) {
	return s.buildOperations.CompletePrepared(operationID, buildID, at)
}

func (s *SQLiteStore) SelectAppBuildForComputeTenant(tenantID, appID, buildID string, at time.Time) error {
	return s.catalog.SelectAppBuild(tenantID, appID, buildID, at)
}

func (s *SQLiteStore) CancelAppBuildUpload(userID, appID, operationID string, at time.Time) (AppBuildOperation, error) {
	return s.buildOperations.Cancel(userID, appID, operationID, at)
}

// ImageReferences describes what keeps published Builds in use on this host.
type ImageReferences struct {
	// BuildIDs are attached to an App, selected by an App runtime, or used by a
	// live Session or non-terminated Sandbox.
	BuildIDs map[string]struct{}
	// PendingUploads may publish a Build before attaching it to their App.
	PendingUploads int
}

func (s *SQLiteStore) ImageReferences() (ImageReferences, error) {
	buildIDs, pending, err := s.buildOperations.PublishedBuildReferences()
	if err != nil {
		return ImageReferences{}, err
	}
	machines, err := s.ListActive()
	if err != nil {
		return ImageReferences{}, fmt.Errorf("list Sandboxes: %w", err)
	}
	for _, machine := range machines {
		if machine.BuildID != "" {
			buildIDs[machine.BuildID] = struct{}{}
		}
	}
	return ImageReferences{BuildIDs: buildIDs, PendingUploads: pending}, nil
}
