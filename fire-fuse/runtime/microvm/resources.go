package microvm

import (
	"regexp"

	"github.com/stepandel/cantelop/fire-fuse/runtime/model"
)

var (
	ErrAppNotFound    = model.ErrAppNotFound
	ErrInvalidApp     = model.ErrInvalidApp
	resourceIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

type App = model.App

// ResourceStore persists first-class App and Workspace records.
type ResourceStore interface {
	GetAppRecord(string) (App, error)
	ListApps() ([]App, error)
	ListAllAppBuilds() ([]AppBuild, error)
}
