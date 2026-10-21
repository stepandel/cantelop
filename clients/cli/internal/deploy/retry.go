package deploy

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/stepandel/cantelop/clients/cli/internal/auth"
	"github.com/stepandel/cantelop/clients/cli/internal/boundedio"
	"github.com/stepandel/cantelop/clients/cli/internal/ids"
)

type deploymentRetryState struct {
	AppID                        string `json:"app_id"`
	APIArtifactDigest            string `json:"api_artifact_digest"`
	SessionRuntimeArtifactDigest string `json:"session_runtime_artifact_digest"`
	IdempotencyKey               string `json:"idempotency_key"`
}

var (
	idempotencyKeyPattern  = regexp.MustCompile(`^cli-deploy:[A-Za-z0-9_-]{22}$`)
	releaseProgressPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
)

func newDeploymentIdempotencyKey() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return "cli-deploy:" + base64.RawURLEncoding.EncodeToString(value), nil
}

func RetryIdentity(appID, apiDigest, sessionRuntimeDigest string) (string, string, error) {
	if !ids.ValidAppID(appID) || !ids.DockerImageIDPattern.MatchString(apiDigest) || !ids.DockerImageIDPattern.MatchString(sessionRuntimeDigest) {
		return "", "", errors.New("invalid deployment retry identity")
	}
	directory := filepath.Join(filepath.Dir(auth.CredentialsPath()), "deployment-retries")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", "", err
	}
	fingerprint := digestBytes([]byte(appID + "\x00" + apiDigest + "\x00" + sessionRuntimeDigest))
	path := filepath.Join(directory, strings.TrimPrefix(fingerprint, "sha256:")+".json")
	want := deploymentRetryState{AppID: appID, APIArtifactDigest: apiDigest, SessionRuntimeArtifactDigest: sessionRuntimeDigest}
	for {
		_, statErr := os.Lstat(path)
		if statErr == nil {
			document, err := boundedio.ReadRegularFile(path, 4<<10)
			if err != nil {
				return "", "", err
			}
			var state deploymentRetryState
			decoder := json.NewDecoder(bytes.NewReader(document))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&state) != nil || decoder.Decode(&struct{}{}) != io.EOF ||
				state.AppID != want.AppID || state.APIArtifactDigest != want.APIArtifactDigest ||
				state.SessionRuntimeArtifactDigest != want.SessionRuntimeArtifactDigest ||
				!idempotencyKeyPattern.MatchString(state.IdempotencyKey) {
				return "", "", errors.New("deployment retry state is invalid")
			}
			return state.IdempotencyKey, path, nil
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return "", "", statErr
		}
		key, err := newDeploymentIdempotencyKey()
		if err != nil {
			return "", "", err
		}
		want.IdempotencyKey = key
		document, err := json.Marshal(want)
		if err != nil {
			return "", "", err
		}
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", "", err
		}
		if _, err = file.Write(document); err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			_ = os.Remove(path)
			return "", "", err
		}
		return key, path, nil
	}
}

func ClearCancelledRetry(appID, key string) error {
	if key == "" {
		return nil
	}
	directory := filepath.Join(filepath.Dir(auth.CredentialsPath()), "deployment-retries")
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		data, err := boundedio.ReadRegularFile(path, 4<<10)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		var state deploymentRetryState
		if json.Unmarshal(data, &state) != nil || state.AppID != appID || state.IdempotencyKey != key {
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
