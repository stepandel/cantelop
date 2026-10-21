package devruntime

import (
	"net/http"
	"regexp"

	"github.com/stepandel/cantelop/clients/cli/internal/localhttp"
)

var localWorkspaceSyncPath = regexp.MustCompile(`^/__cantelop/integration/v2/workspaces/(wsp_[0-9a-f]{32})/sync$`)

func (runtime *localRuntime) serveWorkspaceSync(w http.ResponseWriter, r *http.Request) bool {
	match := localWorkspaceSyncPath.FindStringSubmatch(r.URL.Path)
	if match == nil {
		return false
	}
	runtime.mu.Lock()
	_, exists := runtime.workspaces[match[1]]
	runtime.mu.Unlock()
	if !exists {
		localhttp.WriteError(w, 404, "resource_not_found")
		return true
	}
	runtime.syncFiles.Serve(w, r, match[1])
	return true
}
