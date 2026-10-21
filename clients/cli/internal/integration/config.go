// Package integration records the App integration profiles the SDK client reads.
package integration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"

	"github.com/stepandel/cantelop/clients/cli/internal/boundedio"
	"github.com/stepandel/cantelop/clients/cli/internal/controlapi"
	"github.com/stepandel/cantelop/clients/cli/internal/projectconfig"
)

type AppConfiguration struct {
	ID          string `json:"id"`
	Slug        string `json:"slug"`
	EdgeURL     string `json:"edgeUrl"`
	AccessToken string `json:"accessToken"`
	RuntimeID   string `json:"runtimeId"`
}

type Profile struct {
	DefaultApp map[string]string  `json:"defaultApp,omitempty"`
	Apps       []AppConfiguration `json:"apps"`
}

type Configuration struct {
	SchemaVersion int                `json:"schemaVersion"`
	ActiveProfile string             `json:"activeProfile"`
	Profiles      map[string]Profile `json:"profiles"`
}

func integrationConfigurationPath() (string, error) {
	if configured := os.Getenv("CANTELOP_INTEGRATION_CONFIG"); configured != "" {
		if !filepath.IsAbs(configured) {
			return "", errors.New("CANTELOP_INTEGRATION_CONFIG must be absolute")
		}
		return configured, nil
	}
	directory, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, "cantelop", "integration.json"), nil
}

func SaveApp(profile string, app AppConfiguration) error {
	return updateIntegrationConfiguration(true, func(config *Configuration) bool {
		selected := config.Profiles[profile]
		found := false
		for i, existing := range selected.Apps {
			if existing.ID == app.ID || existing.Slug == app.Slug {
				selected.Apps[i] = app
				found = true
				break
			}
		}
		if !found {
			selected.Apps = append(selected.Apps, app)
		}
		selected.DefaultApp = map[string]string{"slug": app.Slug}
		config.Profiles[profile] = selected
		config.ActiveProfile = profile
		return true
	})
}

// ReplaceToken updates an App already recorded in a profile, keeping
// its runtime identity. It reports whether the App was recorded there.
func ReplaceToken(profile, appID, token string) (bool, error) {
	replaced := false
	err := updateIntegrationConfiguration(false, func(config *Configuration) bool {
		selected, ok := config.Profiles[profile]
		if !ok {
			return false
		}
		for i, existing := range selected.Apps {
			if existing.ID == appID {
				selected.Apps[i].AccessToken = token
				replaced = true
			}
		}
		return replaced
	})
	return replaced, err
}

// updateIntegrationConfiguration serializes profile changes across CLI
// processes. A missing file is created only when create is set.
func updateIntegrationConfiguration(create bool, update func(*Configuration) bool) error {
	path, err := integrationConfigurationPath()
	if err != nil {
		return err
	}
	if !create {
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	unlock, err := lockIntegrationFile(path + ".lock")
	if err != nil {
		return err
	}
	defer unlock()
	config := Configuration{SchemaVersion: 1, Profiles: map[string]Profile{}}
	document, err := boundedio.ReadRegularFile(path, 1<<20)
	if err == nil {
		if err := projectconfig.DecodeJSON(document, &config); err != nil {
			return fmt.Errorf("read integration profiles: %w", err)
		}
		if config.SchemaVersion != 1 || config.Profiles == nil {
			return errors.New("invalid integration profiles")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	} else if !create {
		return nil
	}
	if !update(&config) {
		return nil
	}
	document, err = json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".integration-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(append(document, '\n')); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	// Windows needs the destination removed before rename; Unix replacement is atomic.
	if runtime.GOOS == "windows" {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return os.Rename(file.Name(), path)
}

func ConfigureLocal(config projectconfig.Config, port int, bindingsFile, bridgeOrigin string) error {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return err
	}
	token := hex.EncodeToString(bytes)
	document, err := boundedio.ReadRegularFile(bindingsFile, 1<<20)
	if err != nil {
		return err
	}
	var bindings map[string]string
	if err := json.Unmarshal(document, &bindings); err != nil {
		return err
	}
	bindings["CANTELOP_INTEGRATION_TOKEN"] = token
	bindings["CANTELOP_DEFAULT_KEEP_ALIVE_SECONDS"] = "900"
	bindings["CANTELOP_LOCAL_DATABASE_ORIGIN"] = bridgeOrigin
	document, err = json.Marshal(bindings)
	if err != nil {
		return err
	}
	if err := os.WriteFile(bindingsFile, document, 0o600); err != nil {
		return err
	}
	return SaveApp("local", AppConfiguration{ID: projectconfig.LocalAppID(config.AppSlug), Slug: config.AppSlug, EdgeURL: fmt.Sprintf("http://127.0.0.1:%d", port), AccessToken: token, RuntimeID: config.RuntimeID})
}

func ConfigureDeployed(ctx context.Context, config projectconfig.Config, appID string, session *controlapi.Session) error {
	var credential struct {
		Token string `json:"token"`
		Slug  string `json:"slug"`
	}
	if err := session.Request(ctx, http.MethodPost, "/v1/apps/"+appID+"/integration", "application/json", nil, &credential); err != nil {
		return err
	}
	if credential.Slug != config.AppSlug || len(credential.Token) != 64 {
		return errors.New("invalid App integration credential")
	}
	return SaveApp("default", AppConfiguration{ID: appID, Slug: config.AppSlug, EdgeURL: "https://" + config.AppSlug + ".cantelop.dev", AccessToken: credential.Token, RuntimeID: config.RuntimeID})
}
