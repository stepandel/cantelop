package microvm

import "github.com/stepandel/cantelop/fire-fuse/runtime/model"

const maxWorkspaceIDLength = model.MaxWorkspaceIDLength

var ErrInvalidWorkspaceID = model.ErrInvalidWorkspaceID

// ValidateWorkspaceID checks the opaque identifier used to select persistent
// workspace storage. Workspace IDs are deliberately not filesystem paths.
func ValidateWorkspaceID(id string) error {
	return model.ValidateWorkspaceID(id)
}
