package microvm

import "github.com/stepandel/cantelop/fire-fuse/runtime/model"

// ValidateBuildID retains the existing Runtime API while the canonical
// identifier contract is owned by model.
func ValidateBuildID(buildID string) error {
	return model.ValidateBuildID(buildID)
}
