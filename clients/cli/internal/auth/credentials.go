// Package auth stores the CLI login and runs the browser login flow.
package auth

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/stepandel/cantelop/clients/cli/internal/controlapi"
)

func CredentialsPath() string {
	if configured := strings.TrimSpace(os.Getenv("CANTELOP_CONFIG")); configured != "" {
		return configured
	}
	directory, err := os.UserConfigDir()
	if err != nil {
		return filepath.Join(".", ".cantelop.json")
	}
	return filepath.Join(directory, "cantelop", "config.json")
}

func SaveCredentials(path string, credentials controlapi.Credentials) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	document, err := json.Marshal(credentials)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".config-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(document); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func LoadCredentials(path string) (controlapi.Credentials, error) {
	var credentials controlapi.Credentials
	info, err := os.Stat(path)
	if err != nil {
		return credentials, errors.New("not logged in; run cantelop login")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return credentials, errors.New("Cantelop credential file must not be accessible by group or others")
	}
	document, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(document, &credentials) != nil {
		return credentials, errors.New("Cantelop credential file is invalid; run cantelop login")
	}
	consoleOrigin, consoleErr := controlapi.ValidatePublicOrigin(credentials.ConsoleURL)
	controlOrigin, controlErr := controlapi.ValidatePublicOrigin(credentials.ControlURL)
	if consoleErr != nil || controlErr != nil || consoleOrigin != credentials.ConsoleURL || controlOrigin != credentials.ControlURL || credentials.RefreshToken == "" {
		return credentials, errors.New("Cantelop credential file is invalid; run cantelop login")
	}
	return credentials, nil
}
