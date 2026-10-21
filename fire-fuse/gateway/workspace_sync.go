package gateway

import (
	"errors"
	"io"
	"net/http"
	"regexp"

	"github.com/stepandel/cantelop/fire-fuse/edgeprotocol"
	"github.com/stepandel/cantelop/fire-fuse/syncprotocol"
)

var workspaceSyncPath = regexp.MustCompile(`^/__cantelop/integration/v2/workspaces/(wsp_[0-9a-f]{32})/sync$`)

func (h *Handler) serveWorkspaceSync(w http.ResponseWriter, r *http.Request, target edgeprotocol.DispatchTarget) bool {
	match := workspaceSyncPath.FindStringSubmatch(r.URL.Path)
	if match == nil {
		return false
	}
	if r.Method != "GET" && r.Method != "PUT" && r.Method != "DELETE" {
		writeError(w, 405, "runtime_method_not_allowed", "Unsupported sync method")
		return true
	}
	if _, ok := h.edgeWorkspace(w, r, target.AppID, match[1]); !ok {
		return true
	}
	if h.syncFiles == nil {
		writeError(w, 503, "sync_unavailable", "Workspace sync is unavailable")
		return true
	}
	if r.ContentLength > syncprotocol.MaxFileBytes {
		writeError(w, 413, "sync_file_too_large", "File exceeds the sync size limit")
		return true
	}
	body := http.MaxBytesReader(w, r.Body, syncprotocol.MaxFileBytes)
	r.Body = body
	response, err := h.syncFiles.SyncWorkspace(r.Context(), target.AppID, match[1], r)
	if err != nil {
		// A streamed body over the limit aborts the upstream request. The reader
		// keeps returning its limit error, which distinguishes it from an outage.
		var tooLarge *http.MaxBytesError
		if _, readErr := body.Read(make([]byte, 1)); errors.As(readErr, &tooLarge) {
			writeError(w, 413, "sync_file_too_large", "File exceeds the sync size limit")
			return true
		}
		writeError(w, 503, "sync_unavailable", "Workspace sync is unavailable")
		return true
	}
	defer response.Body.Close()
	// Never truncate a response after committing to its status: the runtime
	// sends explicit lengths, so reject unknown or oversized bodies up front.
	if response.ContentLength < 0 || response.ContentLength > syncprotocol.MaxManifestBytes {
		writeError(w, 502, "sync_unavailable", "Workspace sync response is invalid")
		return true
	}
	for _, name := range []string{"Content-Type", "ETag"} {
		if value := response.Header.Get(name); value != "" {
			w.Header().Set(name, value)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(response.Body, response.ContentLength))
	return true
}
