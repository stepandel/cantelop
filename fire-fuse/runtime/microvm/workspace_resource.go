package microvm

import (
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/model"
)

var (
	ErrInvalidWorkspace  = model.ErrInvalidWorkspace
	ErrWorkspaceNotFound = model.ErrWorkspaceNotFound
)

type Workspace = model.Workspace

// WorkspaceStore persists the App Session runtime projection and the lifecycle of
// addressable App-Workspace bindings.
type WorkspaceStore interface {
	GetAppSessionRuntime(appID string) (AppSessionRuntime, error)
	ListAppBuildsForApp(appID string) ([]AppBuild, error)
	CountWorkspaces(appID string) (int, error)
	CreateWorkspace(Workspace) error
	GetWorkspace(workspaceID string) (Workspace, error)
	GetWorkspaceByAppIDAndSlug(appID, workspaceSlug string) (Workspace, error)
	ListWorkspaces(appID string) ([]Workspace, error)
	ArchiveWorkspace(workspaceID string, at time.Time) (Workspace, error)
	PurgeWorkspaceMetadata(workspaceID string) error
}

type WorkspaceRuntimeConfig = model.WorkspaceRuntimeConfig
