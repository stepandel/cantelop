package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
)

type appResponse struct {
	ID             string                    `json:"id"`
	Slug           string                    `json:"slug"`
	SessionRuntime microvm.AppSessionRuntime `json:"session_runtime"`
	Builds         []appBuildResponse        `json:"builds"`
	CreatedAt      time.Time                 `json:"created_at"`
	UpdatedAt      time.Time                 `json:"updated_at"`
}

type appBuildResponse struct {
	Version   int       `json:"version"`
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
}

type appListResponse struct {
	Items []appResponse `json:"items"`
}

type appProjectionReader interface {
	GetAppSessionRuntime(string) (microvm.AppSessionRuntime, error)
	ListAppBuildsForApp(string) ([]microvm.AppBuild, error)
}

func listApps(controller ResourceController) http.HandlerFunc {
	if controller == nil {
		return unavailable("not_ready", "App storage is unavailable")
	}
	return func(w http.ResponseWriter, _ *http.Request) {
		apps, err := controller.ListAppResources()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "could not list Apps")
			return
		}
		items := make([]appResponse, len(apps))
		for i := range apps {
			items[i], err = readAppResponse(controller, apps[i])
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal_error", "could not list Apps")
				return
			}
		}
		writeJSON(w, http.StatusOK, appListResponse{Items: items})
	}
}

func getApp(controller ResourceController) http.HandlerFunc {
	if controller == nil {
		return unavailable("not_ready", "App storage is unavailable")
	}
	return func(w http.ResponseWriter, r *http.Request) {
		app, err := controller.GetAppResource(r.PathValue("id"))
		if errors.Is(err, microvm.ErrAppNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "App not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "could not retrieve App")
			return
		}
		response, err := readAppResponse(controller, app)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "could not retrieve App")
			return
		}
		writeJSON(w, http.StatusOK, response)
	}
}

func readAppResponse(controller appProjectionReader, app microvm.App) (appResponse, error) {
	sessionRuntime, err := controller.GetAppSessionRuntime(app.ID)
	if err != nil {
		return appResponse{}, err
	}
	builds, err := controller.ListAppBuildsForApp(app.ID)
	if err != nil {
		return appResponse{}, err
	}
	response := appResponse{
		ID: app.ID, Slug: app.Slug, SessionRuntime: sessionRuntime, Builds: make([]appBuildResponse, len(builds)),
		CreatedAt: app.CreatedAt, UpdatedAt: app.UpdatedAt,
	}
	for i := range builds {
		response.Builds[i] = appBuildResponse{Version: builds[i].Version, ID: builds[i].BuildID, CreatedAt: builds[i].CreatedAt}
	}
	return response, nil
}
