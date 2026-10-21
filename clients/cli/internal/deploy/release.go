package deploy

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/stepandel/cantelop/clients/cli/internal/boundedio"
	"github.com/stepandel/cantelop/clients/cli/internal/controlapi"
	"github.com/stepandel/cantelop/clients/cli/internal/ids"
	"github.com/stepandel/cantelop/clients/cli/internal/projectconfig"
)

type terminalDeploymentError struct{ error }

const (
	releaseReadAttempts = 6
)

var (
	releasePollInterval = 2 * time.Second
)

func Release(ctx context.Context, appID, image, artifactDirectory string, output io.Writer, session *controlapi.Session) (resultErr error) {
	api, err := projectconfig.LoadAPIArtifact(artifactDirectory)
	if err != nil {
		return fmt.Errorf("load API artifact: %w", err)
	}
	sessionRuntime, err := packageSessionRuntimeArchive(ctx, image)
	if err != nil {
		return err
	}
	defer os.Remove(sessionRuntime.path)
	apiDigest := digestBytes(api.Module)
	idempotencyKey, retryPath, err := RetryIdentity(appID, apiDigest, sessionRuntime.digest)
	if err != nil {
		return fmt.Errorf("create deployment request identity: %w", err)
	}
	defer func() {
		var terminal terminalDeploymentError
		if resultErr == nil || errors.As(resultErr, &terminal) {
			if removeErr := os.Remove(retryPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				resultErr = errors.Join(resultErr, fmt.Errorf("clear deployment retry state: %w", removeErr))
			}
		}
	}()
	manifest := map[string]any{
		"api": map[string]any{
			"digest":     apiDigest,
			"size_bytes": len(api.Module),
			"manifest":   api.Manifest,
		},
		"session_runtime": map[string]any{
			"digest":     sessionRuntime.digest,
			"size_bytes": sessionRuntime.size,
			"media_type": "application/vnd.cantelop.session-runtime.image.v1.tar+gzip",
		},
	}
	if len(api.ManifestDocument) > 0 {
		manifest["api"].(map[string]any)["manifest"] = api.ManifestDocument
	}
	if api.Manifest.Kind == "cantelop-protocol-edge" {
		runtimeDocument, err := boundedio.ReadRegularFile(filepath.Join(artifactDirectory, "cantelop-runtime.json"), 2<<20)
		if err != nil {
			return err
		}
		manifest["session_runtime"].(map[string]any)["manifest"] = json.RawMessage(runtimeDocument)
	}
	document, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	var upload controlapi.DeploymentUpload
	if err := session.RequestWithHeaders(ctx, http.MethodPost,
		"/v1/apps/"+appID+"/deployments/uploads", "application/json", bytes.NewReader(document), &upload,
		"Idempotency-Key", idempotencyKey); err != nil {
		if controlapi.IsHTTPStatus(err, http.StatusConflict) {
			return fmt.Errorf("begin deployment upload: %w; if an abandoned upload is blocking this App, run cantelop deploy cancel --app %s", err, appID)
		}
		return fmt.Errorf("begin deployment upload: %w", err)
	}
	if err := controlapi.ValidateDeploymentUpload(upload, appID); err != nil {
		return err
	}
	if upload.Status == "finalized" {
		return pollRelease(ctx, session, appID, upload.ReleaseID, output)
	}
	_, _ = fmt.Fprintf(output, "Uploading deployment bundle %s\n", upload.ID)
	if err := controlapi.UploadBytes(ctx, session.Client, *upload.API, api.Module); err != nil {
		return fmt.Errorf("upload API artifact: %w", err)
	}
	parts, err := controlapi.UploadSessionRuntimeParts(ctx, session.Client, sessionRuntime.path, sessionRuntime.size, upload)
	if err != nil {
		return fmt.Errorf("upload Session runtime artifact: %w", err)
	}
	completion, err := json.Marshal(map[string]any{"session_runtime_parts": parts})
	if err != nil {
		return err
	}
	var release controlapi.Release
	if err := session.Request(ctx, http.MethodPost,
		"/v1/apps/"+appID+"/deployments/uploads/"+upload.ID+"/complete",
		"application/json", bytes.NewReader(completion), &release); err != nil {
		return fmt.Errorf("finalize deployment: %w", err)
	}
	if err := controlapi.ValidateRelease(release, appID); err != nil {
		return err
	}
	if release.APIArtifactDigest != apiDigest || release.SessionRuntimeArtifactDigest != sessionRuntime.digest {
		return errors.New("control plane returned a deployment for different artifacts")
	}
	_, _ = fmt.Fprintf(output, "Queued deployment v%d (%s) on %s\n", release.Version, release.ID, appID)
	if release.Status == "active" {
		_, _ = fmt.Fprintf(output, "Activated deployment v%d (%s) on %s\n", release.Version, release.ID, appID)
		return nil
	}
	return pollRelease(ctx, session, appID, release.ID, output)
}

type packagedSessionRuntime struct {
	path   string
	size   int64
	digest string
}

func packageSessionRuntimeArchive(ctx context.Context, image string) (packagedSessionRuntime, error) {
	imageID, err := resolveDockerImageID(ctx, image)
	if err != nil {
		return packagedSessionRuntime{}, err
	}
	file, err := os.CreateTemp("", "cantelop-session-runtime-*.tar.gz")
	if err != nil {
		return packagedSessionRuntime{}, fmt.Errorf("create Session runtime archive: %w", err)
	}
	path := file.Name()
	keep := false
	defer func() {
		_ = file.Close()
		if !keep {
			_ = os.Remove(path)
		}
	}()
	hash := sha256.New()
	compressor, err := gzip.NewWriterLevel(io.MultiWriter(file, hash), gzip.BestSpeed)
	if err != nil {
		return packagedSessionRuntime{}, fmt.Errorf("prepare Session runtime compression: %w", err)
	}
	command := exec.CommandContext(ctx, "docker", "image", "save", imageID)
	stderr := boundedio.NewLimitedBuffer(64 << 10)
	command.Stdout = compressor
	command.Stderr = stderr
	runErr := command.Run()
	compressionErr := compressor.Close()
	if runErr != nil {
		return packagedSessionRuntime{}, fmt.Errorf("docker image save failed: %s", strings.TrimSpace(stderr.String()))
	}
	if compressionErr != nil {
		return packagedSessionRuntime{}, fmt.Errorf("compress Docker archive: %w", compressionErr)
	}
	if err := file.Sync(); err != nil {
		return packagedSessionRuntime{}, fmt.Errorf("sync Session runtime archive: %w", err)
	}
	info, err := file.Stat()
	if err != nil || info.Size() < 1 || info.Size() > 20*1024*1024*1024 {
		return packagedSessionRuntime{}, errors.New("compressed Session runtime archive has an invalid size")
	}
	if err := file.Close(); err != nil {
		return packagedSessionRuntime{}, fmt.Errorf("close Session runtime archive: %w", err)
	}
	keep = true
	return packagedSessionRuntime{path: path, size: info.Size(), digest: "sha256:" + fmt.Sprintf("%x", hash.Sum(nil))}, nil
}

func pollRelease(ctx context.Context, session *controlapi.Session, appID, releaseID string, output io.Writer) error {
	if !ids.ReleaseIDPattern.MatchString(releaseID) {
		return errors.New("control plane returned an invalid deployment ID")
	}
	lastStatus := ""
	for {
		var release controlapi.Release
		// A transient read failure says nothing about the release; keep polling.
		if err := controlapi.RetryTransient(ctx, releaseReadAttempts, func() error {
			return session.Request(ctx, http.MethodGet, "/v1/apps/"+appID+"/deployments/"+releaseID, "", nil, &release)
		}); err != nil {
			return fmt.Errorf("read deployment: %w", err)
		}
		if err := controlapi.ValidateRelease(release, appID); err != nil || release.ID != releaseID {
			return errors.New("control plane returned the wrong deployment")
		}
		progress := fmt.Sprintf("%s: %s (API %s, Session runtime %s)",
			release.ID, release.Status, releaseComponentProgress(release.Components.API), releaseComponentProgress(release.Components.SessionRuntime))
		if progress != lastStatus {
			_, _ = fmt.Fprintln(output, progress)
			lastStatus = progress
		}

		switch release.Status {
		case "active":
			_, _ = fmt.Fprintf(output, "Activated deployment v%d (%s) on %s\n", release.Version, release.ID, appID)
			return nil
		case "failed":
			return terminalDeploymentError{fmt.Errorf("deployment failed: %s", release.ErrorCode)}
		case "queued", "provisioning", "cancelling":
		default:
			return terminalDeploymentError{fmt.Errorf("deployment entered terminal status %q", release.Status)}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(releasePollInterval):
		}
	}
}

func digestBytes(document []byte) string {
	digest := sha256.Sum256(document)
	return "sha256:" + fmt.Sprintf("%x", digest[:])
}

func resolveDockerImageID(ctx context.Context, image string) (string, error) {
	stdout := boundedio.NewLimitedBuffer(1024)
	stderr := boundedio.NewLimitedBuffer(64 << 10)
	command := exec.CommandContext(ctx, "docker", "image", "inspect", "--format={{.Id}}", image)
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return "", fmt.Errorf("resolve Docker image: %s", detail)
	}
	imageID := strings.TrimSpace(stdout.String())
	if !ids.DockerImageIDPattern.MatchString(imageID) {
		return "", errors.New("docker image inspect returned an invalid immutable image ID")
	}
	return imageID, nil
}

func releaseComponentProgress(component controlapi.ReleaseComponent) string {
	if component.Progress != "" && (component.Status == "queued" || component.Status == "running") && releaseProgressPattern.MatchString(component.Progress) {
		progress := component.Progress
		component.Progress = ""
		return releaseComponentProgress(component) + ": " + progress
	}
	progress := component.Status
	if component.Attempts != nil {
		if component.Status == "queued" && *component.Attempts == 0 {
			progress += "; waiting for a worker"
		} else {
			progress += fmt.Sprintf("; attempts %d", *component.Attempts)
		}
	}
	if component.ErrorCode != "" {
		progress += "; " + component.ErrorCode
	}
	if component.UpdatedAt != nil {
		progress += "; last activity " + component.UpdatedAt.Format(time.RFC3339)
	}
	if component.Status == "queued" && component.NextAttemptAt != nil {
		progress += "; eligible from " + component.NextAttemptAt.Format(time.RFC3339)
	}
	return progress
}
