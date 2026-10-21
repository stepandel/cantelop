// Package guestconfig defines the platform-owned configuration used to launch
// one container workload inside a fire-fuse guest.
package guestconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
)

const (
	SchemaVersion                = 1
	maxDocumentBytes             = 1 << 20
	maxArguments                 = 1024
	maxEnvironment               = 4096
	maxStringBytes               = 64 << 10
	defaultWorkingDir            = "/"
	workspaceMountPoint          = "/workspace"
	InternalPortEnvironmentName  = "CANTELOP_INTERNAL_PORT"
	PlatformRuntimeContainerPath = "/__cantelop/bin/bun"
)

var ErrInvalid = errors.New("invalid guest workload configuration")

// Config is the complete guest workload contract. The host resolves container
// image metadata into this form before the guest starts.
type Config struct {
	SchemaVersion int       `json:"schema_version"`
	Process       Process   `json:"process"`
	Workspace     Workspace `json:"workspace"`
}

// SDKListenerPort returns the platform-owned SDK listener port when the
// workload uses Cantelop's native Message runtime protocol. Internal fixtures that
// do not expose an SDK server may omit it.
func SDKListenerPort(environment []string) (int, bool, error) {
	for _, variable := range environment {
		name, value, found := strings.Cut(variable, "=")
		if !found || name != InternalPortEnvironmentName {
			continue
		}
		port, err := strconv.Atoi(value)
		if err != nil || port < 1 || port > 65535 {
			return 0, false, invalid(InternalPortEnvironmentName + " must be a TCP port between 1 and 65535")
		}
		return port, true, nil
	}
	return 0, false, nil
}

// Process contains the already-resolved process settings the guest must apply.
type Process struct {
	Args             []string `json:"args"`
	Env              []string `json:"env"`
	WorkingDirectory string   `json:"working_directory"`
	UID              uint32   `json:"uid"`
	GID              uint32   `json:"gid"`
}

// Workspace selects whether the platform workspace is visible to the workload.
type Workspace struct {
	Enabled       bool   `json:"enabled"`
	ContainerPath string `json:"container_path"`
}

// Decode strictly reads and validates one configuration document.
func Decode(reader io.Reader) (Config, error) {
	if reader == nil {
		return Config{}, invalid("document reader is required")
	}
	document, err := io.ReadAll(io.LimitReader(reader, maxDocumentBytes+1))
	if err != nil {
		return Config{}, fmt.Errorf("%w: read document: %v", ErrInvalid, err)
	}
	if len(document) > maxDocumentBytes {
		return Config{}, invalid("document exceeds 1 MiB")
	}

	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	var config Config
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("%w: decode JSON: %v", ErrInvalid, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Config{}, invalid("document must contain exactly one JSON object")
	}
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

// Validate checks that a configuration can be applied without guessing guest
// process or mount behavior.
func (c Config) Validate() error {
	if c.SchemaVersion != SchemaVersion {
		return invalid(fmt.Sprintf("schema_version must be %d", SchemaVersion))
	}
	if len(c.Process.Args) == 0 {
		return invalid("process args must not be empty")
	}
	if len(c.Process.Args) > maxArguments {
		return invalid(fmt.Sprintf("process args exceed %d entries", maxArguments))
	}
	for index, argument := range c.Process.Args {
		if err := validateString(fmt.Sprintf("process args[%d]", index), argument); err != nil {
			return err
		}
	}
	if c.Process.Args[0] == "" {
		return invalid("process args[0] must not be empty")
	}
	if c.Process.Args[0] == PlatformRuntimeContainerPath && len(c.Process.Args) != 2 {
		return invalid("platform SDK runtime requires exactly one Session runtime artifact argument")
	}

	if err := validateEnvironment("process env", c.Process.Env); err != nil {
		return err
	}

	if c.Process.WorkingDirectory == "" {
		return invalid(fmt.Sprintf("process working_directory must be %q or another absolute path", defaultWorkingDir))
	}
	if err := validateContainerPath("process working_directory", c.Process.WorkingDirectory, true); err != nil {
		return err
	}
	if c.Process.UID == 0 || c.Process.GID == 0 {
		return invalid("process uid and gid must be non-zero")
	}

	if c.Workspace.Enabled {
		if c.Workspace.ContainerPath == "" {
			return invalid(fmt.Sprintf("workspace container_path must be %q or another absolute path", workspaceMountPoint))
		}
		if err := validateContainerPath("workspace container_path", c.Workspace.ContainerPath, false); err != nil {
			return err
		}
	} else if c.Workspace.ContainerPath != "" {
		return invalid("workspace container_path must be empty when workspace is disabled")
	}
	return c.validateSessionContract()
}

// MergeEnvironment applies App-owned overrides to immutable image defaults.
// Existing names retain their position and new names are appended in override
// order so the resulting process contract is deterministic.
func MergeEnvironment(base, overrides []string) ([]string, error) {
	if err := validateEnvironment("base environment", base); err != nil {
		return nil, err
	}
	if err := validateEnvironment("App environment", overrides); err != nil {
		return nil, err
	}
	merged := append([]string(nil), base...)
	positions := make(map[string]int, len(merged))
	for index, variable := range merged {
		name, _, _ := strings.Cut(variable, "=")
		positions[name] = index
	}
	for _, variable := range overrides {
		name, _, _ := strings.Cut(variable, "=")
		if index, exists := positions[name]; exists {
			merged[index] = variable
			continue
		}
		if len(merged) >= maxEnvironment {
			return nil, invalid(fmt.Sprintf("merged environment exceeds %d entries", maxEnvironment))
		}
		positions[name] = len(merged)
		merged = append(merged, variable)
	}
	return merged, nil
}

func validateEnvironment(field string, environment []string) error {
	if len(environment) > maxEnvironment {
		return invalid(fmt.Sprintf("%s exceeds %d entries", field, maxEnvironment))
	}
	names := make(map[string]struct{}, len(environment))
	for index, variable := range environment {
		if err := validateString(fmt.Sprintf("%s[%d]", field, index), variable); err != nil {
			return err
		}
		name, _, found := strings.Cut(variable, "=")
		if !found || name == "" {
			return invalid(fmt.Sprintf("%s[%d] must use NAME=value form", field, index))
		}
		if _, exists := names[name]; exists {
			return invalid(fmt.Sprintf("%s contains duplicate name %q", field, name))
		}
		names[name] = struct{}{}
	}
	return nil
}

func validateString(field, value string) error {
	if len(value) > maxStringBytes {
		return invalid(fmt.Sprintf("%s exceeds %d bytes", field, maxStringBytes))
	}
	if strings.ContainsRune(value, '\x00') {
		return invalid(fmt.Sprintf("%s contains a NUL byte", field))
	}
	return nil
}

func validateContainerPath(field, value string, allowRoot bool) error {
	if err := validateString(field, value); err != nil {
		return err
	}
	if !path.IsAbs(value) || path.Clean(value) != value {
		return invalid(fmt.Sprintf("%s must be a clean absolute path", field))
	}
	if !allowRoot && value == "/" {
		return invalid(fmt.Sprintf("%s cannot be the filesystem root", field))
	}
	return nil
}

func invalid(detail string) error {
	return fmt.Errorf("%w: %s", ErrInvalid, detail)
}
