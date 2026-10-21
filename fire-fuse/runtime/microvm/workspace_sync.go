package microvm

import (
	"net/http"

	"github.com/stepandel/cantelop/fire-fuse/workspacesync"
)

// ConfigureWorkspaceSync is called at startup before the HTTP listener opens.
func (s *Service) ConfigureWorkspaceSync(files *workspacesync.Store) { s.syncFiles = files }

func (s *Service) ServeWorkspaceSync(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("workspace_id")
	appID := r.PathValue("app_id")
	s.syncFiles.ServeAuthorized(w, r, id, func() bool {
		workspace, err := s.store.GetWorkspace(id)
		if err != nil || workspace.AppID != appID || workspace.ArchivedAt != nil {
			return false
		}
		_, err = s.store.GetAppRecord(appID)
		return err == nil
	})
}
