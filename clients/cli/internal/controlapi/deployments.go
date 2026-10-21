package controlapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/stepandel/cantelop/clients/cli/internal/ids"
)

type UploadTarget struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

type SessionRuntimeUploadPart struct {
	PartNumber int               `json:"part_number"`
	Method     string            `json:"method"`
	URL        string            `json:"url"`
	Headers    map[string]string `json:"headers"`
}

type DeploymentUpload struct {
	ID             string        `json:"id"`
	AppID          string        `json:"app_id"`
	Status         string        `json:"status"`
	ExpiresAt      time.Time     `json:"expires_at"`
	ReleaseID      string        `json:"release_id,omitempty"`
	API            *UploadTarget `json:"api,omitempty"`
	SessionRuntime *struct {
		PartSizeBytes int64                      `json:"part_size_bytes"`
		Parts         []SessionRuntimeUploadPart `json:"parts"`
	} `json:"session_runtime,omitempty"`
}

type ReleaseComponent struct {
	Progress      string     `json:"progress,omitempty"`
	Attempts      *int       `json:"attempts,omitempty"`
	UpdatedAt     *time.Time `json:"updated_at,omitempty"`
	NextAttemptAt *time.Time `json:"next_attempt_at,omitempty"`
	Status        string     `json:"status"`
	ErrorCode     string     `json:"error_code,omitempty"`
}

type Release struct {
	ID                           string `json:"id"`
	AppID                        string `json:"app_id"`
	Version                      int    `json:"version"`
	Status                       string `json:"status"`
	APIArtifactDigest            string `json:"api_artifact_digest"`
	SessionRuntimeArtifactDigest string `json:"session_runtime_artifact_digest"`
	Components                   struct {
		API            ReleaseComponent `json:"api"`
		SessionRuntime ReleaseComponent `json:"session_runtime"`
	} `json:"components"`
	ErrorCode   string     `json:"error_code,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	ActivatedAt *time.Time `json:"activated_at,omitempty"`
}

type CompletedSessionRuntimePart struct {
	PartNumber int    `json:"part_number"`
	ETag       string `json:"etag"`
}

func UploadSessionRuntimeParts(
	ctx context.Context,
	client *http.Client,
	path string,
	size int64,
	upload DeploymentUpload,
) ([]CompletedSessionRuntimePart, error) {
	if upload.SessionRuntime == nil || upload.SessionRuntime.PartSizeBytes < 5<<20 || len(upload.SessionRuntime.Parts) == 0 ||
		int64(len(upload.SessionRuntime.Parts)) != (size+upload.SessionRuntime.PartSizeBytes-1)/upload.SessionRuntime.PartSizeBytes {
		return nil, errors.New("control plane returned an invalid Session runtime upload")
	}
	for index, target := range upload.SessionRuntime.Parts {
		if target.PartNumber != index+1 {
			return nil, errors.New("control plane returned unordered Session runtime parts")
		}
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	// Parts upload concurrently; each reads its own section of the archive.
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	completed := make([]CompletedSessionRuntimePart, len(upload.SessionRuntime.Parts))
	slots := make(chan struct{}, SessionRuntimeUploadConcurrency)
	var wait sync.WaitGroup
	for index, target := range upload.SessionRuntime.Parts {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
		wait.Add(1)
		go func() {
			defer wait.Done()
			defer func() { <-slots }()
			offset := int64(index) * upload.SessionRuntime.PartSizeBytes
			length := min(upload.SessionRuntime.PartSizeBytes, size-offset)
			etag, err := uploadPart(ctx, client, target, io.NewSectionReader(file, offset, length), length)
			if err != nil {
				cancel(fmt.Errorf("part %d: %w", target.PartNumber, err))
				return
			}
			completed[index] = CompletedSessionRuntimePart{PartNumber: target.PartNumber, ETag: etag}
		}()
	}
	wait.Wait()
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	return completed, nil
}

func uploadPart(ctx context.Context, client *http.Client, target SessionRuntimeUploadPart, body *io.SectionReader, size int64) (string, error) {
	if target.Method != http.MethodPut {
		return "", errors.New("control plane returned an invalid upload method")
	}
	var etag string
	err := RetryTransient(ctx, uploadAttempts, func() error {
		response, err := executeUpload(ctx, client, target.URL, target.Headers, io.NewSectionReader(body, 0, size), size)
		if err != nil {
			return err
		}
		etag = response.Header.Get("ETag")
		return response.Body.Close()
	})
	if err != nil {
		return "", err
	}
	if !uploadPartETagPattern.MatchString(etag) {
		return "", errors.New("artifact store omitted a valid part ETag")
	}
	return etag, nil
}

func UploadBytes(ctx context.Context, client *http.Client, target UploadTarget, body []byte) error {
	if target.Method != http.MethodPut {
		return errors.New("control plane returned an invalid upload method")
	}
	return RetryTransient(ctx, uploadAttempts, func() error {
		response, err := executeUpload(ctx, client, target.URL, target.Headers, bytes.NewReader(body), int64(len(body)))
		if err != nil {
			return err
		}
		return response.Body.Close()
	})
}

func executeUpload(
	ctx context.Context,
	client *http.Client,
	endpoint string,
	headers map[string]string,
	body io.Reader,
	size int64,
) (*http.Response, error) {
	if _, err := validateUploadURL(endpoint); err != nil || size < 1 {
		return nil, errors.New("control plane returned an invalid upload target")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, body)
	if err != nil {
		return nil, err
	}
	request.ContentLength = size
	for name, value := range headers {
		canonical := http.CanonicalHeaderKey(name)
		if (canonical != "Content-Type" && !strings.HasPrefix(strings.ToLower(name), "x-amz-")) ||
			strings.ContainsAny(value, "\x00\r\n") {
			return nil, errors.New("control plane returned invalid upload headers")
		}
		request.Header.Set(name, value)
	}
	uploadClient := &http.Client{Transport: client.Transport, Jar: client.Jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	response, err := uploadClient.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_ = response.Body.Close()
		return nil, fmt.Errorf("artifact store returned %w", &StatusError{StatusCode: response.StatusCode})
	}
	return response, nil
}

func validateUploadURL(raw string) (*url.URL, error) {
	endpoint, err := url.Parse(raw)
	if err != nil || endpoint.User != nil || endpoint.Host == "" || endpoint.Fragment != "" {
		return nil, errors.New("invalid upload URL")
	}
	local := endpoint.Scheme == "http" && (endpoint.Hostname() == "127.0.0.1" || endpoint.Hostname() == "::1" || endpoint.Hostname() == "localhost")
	if endpoint.Scheme != "https" && !local {
		return nil, errors.New("upload URL must use HTTPS")
	}
	return endpoint, nil
}

func ValidateDeploymentUpload(upload DeploymentUpload, appID string) error {
	if !ids.DeploymentUploadIDPattern.MatchString(upload.ID) || upload.AppID != appID || upload.ExpiresAt.IsZero() {
		return errors.New("control plane returned an invalid deployment upload")
	}
	if upload.Status == "finalized" {
		if !ids.ReleaseIDPattern.MatchString(upload.ReleaseID) || upload.API != nil || upload.SessionRuntime != nil {
			return errors.New("control plane returned an invalid finalized upload")
		}
		return nil
	}
	if upload.Status != "uploading" || upload.API == nil || upload.SessionRuntime == nil || upload.ReleaseID != "" ||
		upload.API.Method != http.MethodPut || upload.SessionRuntime.PartSizeBytes < 5<<20 || len(upload.SessionRuntime.Parts) < 1 {
		return errors.New("control plane returned an invalid deployment upload")
	}
	return nil
}

func ValidateRelease(release Release, appID string) error {
	validStatus := release.Status == "queued" || release.Status == "provisioning" || release.Status == "active" ||
		release.Status == "failed" || release.Status == "superseded" || release.Status == "cancelling" || release.Status == "cancelled"
	validComponent := func(component ReleaseComponent) bool {
		return component.Status == "queued" || component.Status == "running" || component.Status == "succeeded" || component.Status == "failed"
	}
	if !ids.ReleaseIDPattern.MatchString(release.ID) || release.AppID != appID || release.Version < 1 || !validStatus ||
		!ids.DockerImageIDPattern.MatchString(release.APIArtifactDigest) || !ids.DockerImageIDPattern.MatchString(release.SessionRuntimeArtifactDigest) ||
		!validComponent(release.Components.API) || !validComponent(release.Components.SessionRuntime) ||
		(release.Status == "failed") != (release.ErrorCode != "") {
		return errors.New("control plane returned an invalid deployment")
	}
	return nil
}

const (
	SessionRuntimeUploadConcurrency = 4
	uploadAttempts                  = 4
	maxTransientRetryDelay          = 30 * time.Second
)

var (
	uploadPartETagPattern = regexp.MustCompile(`^"?[A-Za-z0-9+/=_-]{1,128}"?$`)
)
