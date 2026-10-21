package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stepandel/cantelop/fire-fuse/config"
	"github.com/stepandel/cantelop/fire-fuse/guest-runtime/guestconfig"
	"github.com/stepandel/cantelop/fire-fuse/runtime/imagecatalog"
	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
	"github.com/stepandel/cantelop/fire-fuse/runtime/model"
)

const preparedCommandMaxBytes = 2 << 20
const maxPreparedImageBytes int64 = 52 << 30

// The image directory shares its filesystem with the lifecycle database, VM
// jails, and catalog rewrites. Admission keeps a share of it free.
const (
	minImageDiskReserve uint64 = 4 << 30
	maxImageDiskReserve uint64 = 32 << 30
)

var errImageDiskCapacity = errors.New("image filesystem lacks free space for the prepared Build")

var rootFSDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var controlOperationIDPattern = regexp.MustCompile(`^op_([0-9a-f]{32})$`)
var sha256DigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type PreparedBuildController interface {
	ComputeDesiredStateController
	BeginOrGetAppBuildUpload(userID, appID, operationID string) (microvm.AppBuildOperation, bool, error)
	CompletePreparedAppBuild(operationID, buildID string) (microvm.AppBuildOperation, microvm.AppBuild, error)
	CancelAppBuildUpload(userID, appID, operationID string) (microvm.AppBuildOperation, error)
	SelectAppBuildForComputeTenant(userID, appID, buildID string) error
	GetActiveAppBuildForComputeTenant(userID, appID string) (string, error)
	GetAppBuildForComputeTenant(userID, appID, buildID string) (microvm.AppBuild, error)
	GetReleaseAdmissionForComputeTenant(userID, appID string) (microvm.ReleaseAdmission, error)
	SetReleaseAdmissionForComputeTenant(userID, appID, generation string, closed bool) (microvm.ReleaseAdmission, error)
}

type preparedManifest struct {
	SchemaVersion   int                 `json:"schema_version"`
	BuildID         string              `json:"build_id"`
	ImageKey        string              `json:"image_key"`
	RootFSSHA256    string              `json:"rootfs_sha256"`
	RootFSSizeBytes int64               `json:"rootfs_size_bytes"`
	Process         guestconfig.Process `json:"process"`
}

type preparedCommand struct {
	CommandID string           `json:"command_id"`
	ImageURL  string           `json:"image_url"`
	Manifest  preparedManifest `json:"manifest"`
}

type preparedBuildIngress struct {
	control        PreparedBuildController
	artifactOrigin string
	imageDirectory string
	catalogPath    string
	client         *http.Client
	installMu      sync.Mutex
	// statFilesystem reports available and total bytes; nil uses statfs.
	statFilesystem func(string) (available, total uint64, err error)
}

func NewPreparedBuildIngressHandler(control PreparedBuildController, artifactOrigin, imageDirectory, catalogPath string, client *http.Client) (http.Handler, error) {
	origin, err := url.Parse(artifactOrigin)
	if err != nil || origin.Scheme != "https" || origin.User != nil || origin.Host == "" || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return nil, errors.New("artifact download origin must be an HTTPS origin")
	}
	if control == nil || !filepath.IsAbs(imageDirectory) || !filepath.IsAbs(catalogPath) {
		return nil, errors.New("prepared Build controller and absolute image/catalog paths are required")
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Minute}
	}
	// No install runs before the handler exists, so any partial download is
	// left over from a crash.
	if err := removeAbandonedDownloads(imageDirectory); err != nil {
		return nil, err
	}
	h := &preparedBuildIngress{control: control, artifactOrigin: origin.String(), imageDirectory: imageDirectory, catalogPath: catalogPath, client: client}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, struct {
			Status string `json:"status"`
		}{Status: "ok"})
	})
	reader, _ := control.(interface {
		Get(string) (microvm.Sandbox, error)
	})
	mux.HandleFunc("GET /v1/internal/database-access/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if reader == nil || r.URL.RawQuery != "" || microvm.ValidateSandboxID(r.PathValue("id")) != nil {
			writeError(w, 404, "not_found", "sandbox unavailable")
			return
		}
		machine, err := reader.Get(r.PathValue("id"))
		if err != nil || !model.CanAccessDatabase(machine.State) {
			writeError(w, 404, "not_found", "sandbox unavailable")
			return
		}
		writeJSON(w, 200, struct {
			SandboxID   string `json:"sandboxId"`
			AppID       string `json:"appId"`
			WorkspaceID string `json:"workspaceId"`
			Active      bool   `json:"active"`
		}{machine.ID, machine.AppID, machine.WorkspaceID, true})
	})
	managedRoutes(mux, control)
	mux.HandleFunc("PUT /v1/internal/users/{user_id}/apps/{app_id}", registerComputeApp(control))
	mux.HandleFunc("DELETE /v1/internal/users/{user_id}/apps/{app_id}", archiveComputeApp(control))
	mux.HandleFunc("PUT /v1/internal/users/{user_id}/apps/{app_id}/workspaces/{workspace_id}", registerComputeWorkspace(control))
	mux.HandleFunc("DELETE /v1/internal/users/{user_id}/apps/{app_id}/workspaces/{workspace_id}", archiveComputeWorkspace(control))
	mux.HandleFunc("DELETE /v1/internal/users/{user_id}/apps/{app_id}/workspaces/{workspace_id}/purge", purgeComputeWorkspace(control))
	mux.HandleFunc("PUT /v1/internal/users/{user_id}/apps/{app_id}/configuration", replaceComputeConfig(control))
	mux.HandleFunc("POST /v1/internal/users/{user_id}/apps/{app_id}/prepared-builds", h.install)
	mux.HandleFunc("DELETE /v1/internal/users/{user_id}/apps/{app_id}/prepared-builds/{command_id}", h.cancel)
	mux.HandleFunc("PUT /v1/internal/users/{user_id}/apps/{app_id}/active-build", h.selectBuild)
	mux.HandleFunc("GET /v1/internal/users/{user_id}/apps/{app_id}/active-build", h.getActiveBuild)
	mux.HandleFunc("GET /v1/internal/users/{user_id}/apps/{app_id}/builds/{build_id}", h.getBuild)
	mux.HandleFunc("GET /v1/internal/users/{user_id}/apps/{app_id}/release-admission", h.getReleaseAdmission)
	mux.HandleFunc("PUT /v1/internal/users/{user_id}/apps/{app_id}/release-admission", h.setReleaseAdmission)
	return withRequestID(mux), nil
}

func (h *preparedBuildIngress) getReleaseAdmission(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "release admission query is invalid")
		return
	}
	state, err := h.control.GetReleaseAdmissionForComputeTenant(r.PathValue("user_id"), r.PathValue("app_id"))
	if err != nil {
		writeReleaseAdmissionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (h *preparedBuildIngress) setReleaseAdmission(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" || !strings.EqualFold(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]), "application/json") {
		writeError(w, http.StatusBadRequest, "invalid_request", "release admission must be JSON")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 128)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var command struct {
		Generation string `json:"generation"`
		Closed     *bool  `json:"closed"`
	}
	if decoder.Decode(&command) != nil || decoder.Decode(&struct{}{}) != io.EOF || command.Closed == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "release admission command is invalid")
		return
	}
	state, err := h.control.SetReleaseAdmissionForComputeTenant(r.PathValue("user_id"), r.PathValue("app_id"), command.Generation, *command.Closed)
	if err != nil {
		writeReleaseAdmissionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func writeReleaseAdmissionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, microvm.ErrAppNotFound):
		writeError(w, http.StatusNotFound, "not_found", "App not found")
	case errors.Is(err, microvm.ErrReleaseAdmissionConflict):
		writeError(w, http.StatusConflict, "release_admission_conflict", "release admission generation conflict")
	default:
		writeError(w, http.StatusServiceUnavailable, "not_ready", "release admission is unavailable")
	}
}

func (h *preparedBuildIngress) selectBuild(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" || !strings.EqualFold(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]), "application/json") {
		writeError(w, http.StatusBadRequest, "invalid_request", "active Build selection must be JSON")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 128)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var command struct {
		BuildID json.RawMessage `json:"build_id"`
	}
	if decoder.Decode(&command) != nil || decoder.Decode(&struct{}{}) != io.EOF || len(command.BuildID) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "active Build selection is invalid")
		return
	}
	var selected *string
	if string(command.BuildID) != "null" {
		var buildID string
		if json.Unmarshal(command.BuildID, &buildID) != nil || microvm.ValidateBuildID(buildID) != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "active Build selection is invalid")
			return
		}
		selected = &buildID
	}
	appID := r.PathValue("app_id")
	selectedID := ""
	if selected != nil {
		selectedID = *selected
	}
	if err := h.control.SelectAppBuildForComputeTenant(r.PathValue("user_id"), appID, selectedID); err != nil {
		if errors.Is(err, microvm.ErrInvalidAppBuild) {
			writeError(w, http.StatusBadRequest, "invalid_request", "active Build selection is invalid")
			return
		}
		writeRemoteBuildControlError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		AppID   string  `json:"app_id"`
		BuildID *string `json:"build_id"`
	}{AppID: appID, BuildID: selected})
}

func (h *preparedBuildIngress) getActiveBuild(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "active Build query is invalid")
		return
	}
	appID := r.PathValue("app_id")
	buildID, err := h.control.GetActiveAppBuildForComputeTenant(r.PathValue("user_id"), appID)
	if err != nil {
		writeRemoteBuildControlError(w, err)
		return
	}
	var selected *string
	if buildID != "" {
		selected = &buildID
	}
	writeJSON(w, http.StatusOK, struct {
		AppID   string  `json:"app_id"`
		BuildID *string `json:"build_id"`
	}{AppID: appID, BuildID: selected})
}

// getBuild reports whether a Build is still attached to the App and can be
// selected. Retention and image collection make older Builds disappear.
func (h *preparedBuildIngress) getBuild(w http.ResponseWriter, r *http.Request) {
	buildID := r.PathValue("build_id")
	if r.URL.RawQuery != "" || microvm.ValidateBuildID(buildID) != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Build query is invalid")
		return
	}
	appID := r.PathValue("app_id")
	build, err := h.control.GetAppBuildForComputeTenant(r.PathValue("user_id"), appID, buildID)
	if errors.Is(err, microvm.ErrAppBuildNotFound) {
		writeError(w, http.StatusNotFound, "build_not_found", "Build is no longer retained for this App")
		return
	}
	if err != nil {
		writeRemoteBuildControlError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		AppID   string `json:"app_id"`
		BuildID string `json:"build_id"`
		Version int    `json:"version"`
	}{AppID: appID, BuildID: build.BuildID, Version: build.Version})
}

func (h *preparedBuildIngress) install(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" || !strings.EqualFold(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]), "application/json") {
		writeError(w, http.StatusBadRequest, "invalid_request", "prepared Build command must be JSON")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, preparedCommandMaxBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var command preparedCommand
	if decoder.Decode(&command) != nil || decoder.Decode(&struct{}{}) != io.EOF || command.validate(h.artifactOrigin, h.imageDirectory) != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "prepared Build command is invalid")
		return
	}
	h.installMu.Lock()
	defer h.installMu.Unlock()
	operationID := "bop_" + controlOperationIDPattern.FindStringSubmatch(command.CommandID)[1]
	userID, appID := r.PathValue("user_id"), r.PathValue("app_id")
	operation, _, err := h.control.BeginOrGetAppBuildUpload(userID, appID, operationID)
	if err != nil {
		writeRemoteBuildControlError(w, err)
		return
	}
	if operation.Status == microvm.AppBuildOperationSucceeded || operation.Status == microvm.AppBuildOperationFailed {
		writeJSON(w, http.StatusOK, operation)
		return
	}
	if operation.Status != microvm.AppBuildOperationReceiving {
		writeError(w, http.StatusConflict, "invalid_build_state", "prepared Build is not receiving")
		return
	}
	image := config.ContainerImage{
		RootFSImage:     filepath.Join(h.imageDirectory, "sha256-"+command.Manifest.RootFSSHA256+".ext4"),
		RootFSSHA256:    command.Manifest.RootFSSHA256,
		RootFSSizeBytes: command.Manifest.RootFSSizeBytes,
		Process:         command.Manifest.Process,
	}
	if err := h.downloadAndInstall(r.Context(), command.ImageURL, image); err != nil {
		if errors.Is(err, errImageDiskCapacity) {
			writeError(w, http.StatusInsufficientStorage, "host_disk_capacity", "host image storage is full")
			return
		}
		writeError(w, http.StatusServiceUnavailable, "prepared_image_unavailable", "prepared image download or verification failed")
		return
	}
	if _, err := imagecatalog.PublishImage(h.catalogPath, image); err != nil {
		writeError(w, http.StatusServiceUnavailable, "catalog_unavailable", "prepared image could not be published")
		return
	}
	operation, _, err = h.control.CompletePreparedAppBuild(operationID, command.Manifest.BuildID)
	if err != nil {
		// A concurrent identical request may have completed or cancellation may
		// have won. Return the durable receipt in either case.
		operation, _, readErr := h.control.BeginOrGetAppBuildUpload(userID, appID, operationID)
		if readErr == nil && (operation.Status == microvm.AppBuildOperationSucceeded || operation.Status == microvm.AppBuildOperationFailed) {
			writeJSON(w, http.StatusOK, operation)
			return
		}
		writeRemoteBuildControlError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, operation)
}

func (c preparedCommand) validate(origin, imageDirectory string) error {
	if !controlOperationIDPattern.MatchString(c.CommandID) || c.Manifest.SchemaVersion != 1 ||
		!sha256DigestPattern.MatchString(c.Manifest.BuildID) || !rootFSDigestPattern.MatchString(c.Manifest.RootFSSHA256) ||
		c.Manifest.RootFSSizeBytes < 1 || c.Manifest.RootFSSizeBytes > maxPreparedImageBytes {
		return errors.New("invalid prepared Build identity")
	}
	wantKey := "prepared-images/sha256/" + c.Manifest.RootFSSHA256 + ".ext4"
	if c.Manifest.ImageKey != wantKey {
		return errors.New("invalid prepared image key")
	}
	u, err := url.Parse(c.ImageURL)
	if err != nil || u.Scheme+"://"+u.Host != origin || u.User != nil || u.Fragment != "" || u.RawQuery == "" || !strings.HasSuffix(u.Path, "/"+wantKey) {
		return errors.New("invalid signed prepared image URL")
	}
	image := config.ContainerImage{
		RootFSImage:  filepath.Join(imageDirectory, "sha256-"+c.Manifest.RootFSSHA256+".ext4"),
		RootFSSHA256: c.Manifest.RootFSSHA256, RootFSSizeBytes: c.Manifest.RootFSSizeBytes, Process: c.Manifest.Process,
	}
	buildID, err := config.ContainerImageID(image)
	if err != nil || buildID != c.Manifest.BuildID {
		return errors.New("invalid prepared Build ID")
	}
	return nil
}

func (h *preparedBuildIngress) downloadAndInstall(ctx context.Context, source string, image config.ContainerImage) error {
	if err := os.MkdirAll(h.imageDirectory, 0o700); err != nil {
		return err
	}
	// The download always lands in a temporary copy first, even when the
	// content-addressed rootfs already exists.
	if err := h.checkDiskSpace(image.RootFSSizeBytes); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(h.imageDirectory, ".prepared-*.ext4")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	defer temporary.Close()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return err
	}
	client := *h.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || (response.ContentLength >= 0 && response.ContentLength != image.RootFSSizeBytes) {
		return errors.New("prepared image response size mismatch")
	}
	hash := sha256.New()
	size, err := io.CopyN(io.MultiWriter(temporary, hash), response.Body, image.RootFSSizeBytes)
	if err != nil || size != image.RootFSSizeBytes {
		return errors.New("prepared image truncated")
	}
	var extra [1]byte
	if n, readErr := response.Body.Read(extra[:]); n != 0 || readErr != io.EOF {
		return errors.New("prepared image exceeds expected size")
	}
	if hex.EncodeToString(hash.Sum(nil)) != image.RootFSSHA256 {
		return errors.New("prepared image digest mismatch")
	}
	if err := temporary.Chmod(0o644); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := os.Link(temporary.Name(), image.RootFSImage); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("install prepared rootfs: %w", err)
		}
		// PublishImage rehashes the existing file and rejects a conflicting one.
	}
	directory, err := os.Open(h.imageDirectory)
	if err != nil {
		return err
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync prepared image directory: %w", err)
	}
	return nil
}

func (h *preparedBuildIngress) checkDiskSpace(size int64) error {
	stat := h.statFilesystem
	if stat == nil {
		stat = statFilesystem
	}
	available, total, err := stat(h.imageDirectory)
	if err != nil {
		return fmt.Errorf("inspect image filesystem: %w", err)
	}
	reserve := min(max(total/20, minImageDiskReserve), maxImageDiskReserve)
	if available < uint64(size) || available-uint64(size) < reserve {
		return fmt.Errorf("%w: %d bytes available, %d needed plus %d reserved", errImageDiskCapacity, available, size, reserve)
	}
	return nil
}

func statFilesystem(path string) (available, total uint64, err error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, 0, err
	}
	return uint64(stat.Bavail) * uint64(stat.Bsize), uint64(stat.Blocks) * uint64(stat.Bsize), nil
}

func removeAbandonedDownloads(directory string) error {
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("list image directory: %w", err)
	}
	for _, entry := range entries {
		if matched, _ := filepath.Match(".prepared-*.ext4", entry.Name()); !matched || !entry.Type().IsRegular() {
			continue
		}
		if err := os.Remove(filepath.Join(directory, entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove abandoned prepared download: %w", err)
		}
	}
	return nil
}

func (h *preparedBuildIngress) cancel(w http.ResponseWriter, r *http.Request) {
	match := controlOperationIDPattern.FindStringSubmatch(r.PathValue("command_id"))
	if len(match) != 2 || r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid cancellation command")
		return
	}
	op, err := h.control.CancelAppBuildUpload(r.PathValue("user_id"), r.PathValue("app_id"), "bop_"+match[1])
	if err != nil {
		writeRemoteBuildControlError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, op)
}

func writeRemoteBuildControlError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, microvm.ErrAppNotFound), errors.Is(err, microvm.ErrAppBuildOperationNotFound), errors.Is(err, microvm.ErrAppBuildNotFound):
		writeError(w, http.StatusNotFound, "not_found", "App not found")
	case errors.Is(err, microvm.ErrAppBuildUploadPending):
		writeError(w, http.StatusConflict, "build_upload_pending", "App already has a pending Build")
	case errors.Is(err, microvm.ErrReleaseAdmissionConflict):
		writeError(w, http.StatusConflict, "release_admission_conflict", "release admission must be closed for Build selection")
	case errors.Is(err, microvm.ErrAppBuildQuota):
		writeError(w, http.StatusTooManyRequests, "build_retention_quota", "App Build retention quota exceeded")
	case errors.Is(err, microvm.ErrComputeTenantMissing):
		writeError(w, http.StatusNotFound, "compute_tenant_not_found", "compute tenant is not registered")
	default:
		writeError(w, http.StatusServiceUnavailable, "control_unavailable", "Build installation could not complete")
	}
}
