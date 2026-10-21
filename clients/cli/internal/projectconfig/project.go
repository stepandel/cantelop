// Package projectconfig loads the project manifest, discovers its App through
// the project-installed SDK, and validates the artifacts that SDK builds.
package projectconfig

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/stepandel/cantelop/clients/cli/internal/boundedio"
	"github.com/stepandel/cantelop/clients/cli/internal/ids"
)

const MaxConfigInputBytes = 64 << 10

const DefaultPath = "cantelop.json"

const defaultSessionDockerfile = `FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get upgrade --yes \
    && apt-get install --yes --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*
`

type projectConfigDocument struct {
	SchemaVersion int                               `json:"schema_version"`
	Definition    string                            `json:"definition"`
	App           string                            `json:"app"`
	API           json.RawMessage                   `json:"api"`
	Session       json.RawMessage                   `json:"session"`
	Environment   map[string]EnvironmentDeclaration `json:"environment"`
}

type EnvironmentDeclaration struct {
	Default  *string `json:"default"`
	Secret   bool    `json:"secret"`
	Required bool    `json:"required"`
}

type projectAPIConfig struct {
	Entrypoint string `json:"entrypoint"`
}

type projectSessionConfig struct {
	Dockerfile string `json:"dockerfile"`
	Entrypoint string `json:"entrypoint"`
}

type Config struct {
	Definition        string
	AppSelection      string
	RuntimeID         string
	Path              string
	Root              string
	AppSlug           string
	APIEntrypoint     string
	SessionDockerfile string
	SessionEntrypoint string
	Environment       map[string]EnvironmentDeclaration
}

type projectJSONLocation struct {
	Line   int
	Column int
}

type projectManifestError struct {
	File     string
	Field    string
	Location projectJSONLocation
	Message  string
}

func (manifestError projectManifestError) Error() string {
	field := manifestError.Field
	if field == "" {
		field = "configuration"
	}
	return fmt.Sprintf("%s:%d:%d\n%s: %s", manifestError.File, manifestError.Location.Line,
		manifestError.Location.Column, field, manifestError.Message)
}

var (
	unknownJSONFieldPattern = regexp.MustCompile(`^json: unknown field "([^"]+)"$`)
	jsonByteOffsetPattern   = regexp.MustCompile(` at byte offset [0-9]+$`)
)

func Load(path string) (Config, error) {
	return LoadSelected(path, "")
}

func LoadSelected(path, app string) (Config, error) {
	if strings.TrimSpace(path) == "" {
		return Config{}, errors.New("project configuration path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return Config{}, fmt.Errorf("resolve project configuration: %w", err)
	}
	document, err := boundedio.ReadRegularFile(absolute, 64<<10)
	if err != nil {
		return Config{}, fmt.Errorf("read project configuration: %w", err)
	}
	locations, locationErr := projectJSONLocations(document)
	if locationErr != nil {
		return Config{}, projectDecodeError(path, document, nil, locationErr)
	}
	var input projectConfigDocument
	if err := DecodeJSON(document, &input); err != nil {
		return Config{}, projectDecodeError(path, document, locations, err)
	}
	manifestError := func(field, message string) error {
		return newProjectManifestError(path, locations, field, message)
	}
	if input.SchemaVersion != 0 || input.Definition != "" {
		if input.SchemaVersion != 3 {
			return Config{}, manifestError("schema_version", "is not supported")
		}
		if input.Definition == "" || input.App != "" || len(input.API) != 0 || len(input.Session) != 0 || input.Environment != nil {
			return Config{}, manifestError("definition", "schema 3 requires only schema_version and definition; App configuration belongs in the definition")
		}
		root := filepath.Dir(absolute)
		definition, err := resolveProjectPath(root, input.Definition, false)
		if err != nil {
			return Config{}, manifestError("definition", describeProjectPathError(err, false))
		}
		config := Config{Path: absolute, Root: root, Definition: definition, AppSelection: app}
		return discoverProjectApp(config)
	}
	if app != "" && app != input.App {
		return Config{}, manifestError("app", "selected App does not match the project")
	}
	if input.App == "" {
		return Config{}, manifestError("app", "is required")
	}
	if !ids.ValidRoutingSlug(input.App) {
		return Config{}, manifestError("app", "must be a lowercase routing slug without consecutive hyphens")
	}
	api, err := decodeProjectAPI(input.API)
	if err != nil {
		return Config{}, manifestError(projectFieldFromError("api", err), projectMessageFromError(err))
	}
	session, err := decodeProjectSession(input.Session)
	if err != nil {
		return Config{}, manifestError(projectFieldFromError("session", err), projectMessageFromError(err))
	}
	root := filepath.Dir(absolute)
	entrypoint, err := resolveProjectPath(root, api.Entrypoint, false)
	if err != nil {
		return Config{}, manifestError("api.entrypoint", describeProjectPathError(err, false))
	}
	dockerfile := ""
	if session.Dockerfile != "" {
		dockerfile, err = resolveProjectPath(root, session.Dockerfile, false)
		if err != nil {
			return Config{}, manifestError("session.dockerfile", describeProjectPathError(err, false))
		}
	}
	sessionEntrypoint, err := resolveProjectPath(root, session.Entrypoint, false)
	if err != nil {
		return Config{}, manifestError("session.entrypoint", describeProjectPathError(err, false))
	}
	if field, message := validateProjectEnvironment(input.Environment); message != "" {
		return Config{}, manifestError(field, message)
	}
	return Config{
		Path:              absolute,
		Root:              root,
		AppSlug:           input.App,
		APIEntrypoint:     entrypoint,
		SessionDockerfile: dockerfile,
		SessionEntrypoint: sessionEntrypoint,
		Environment:       cloneProjectEnvironment(input.Environment),
	}, nil
}

func decodeProjectAPI(document json.RawMessage) (projectAPIConfig, error) {
	if len(document) == 0 {
		return projectAPIConfig{}, errors.New("api: is required")
	}
	if firstJSONByte(document) == '"' {
		var entrypoint string
		if err := json.Unmarshal(document, &entrypoint); err != nil || entrypoint == "" {
			return projectAPIConfig{}, errors.New("api: must be a non-empty project path")
		}
		return projectAPIConfig{Entrypoint: entrypoint}, nil
	}
	if firstJSONByte(document) != '{' {
		return projectAPIConfig{}, errors.New("api: must be a project path or an object")
	}
	var config projectAPIConfig
	if err := DecodeJSON(document, &config); err != nil {
		return projectAPIConfig{}, fmt.Errorf("api.%s", projectNestedDecodeMessage(err))
	}
	if config.Entrypoint == "" {
		return projectAPIConfig{}, errors.New("api.entrypoint: is required")
	}
	return config, nil
}

func decodeProjectSession(document json.RawMessage) (projectSessionConfig, error) {
	if len(document) == 0 {
		return projectSessionConfig{}, errors.New("session: is required")
	}
	if firstJSONByte(document) == '"' {
		var entrypoint string
		if err := json.Unmarshal(document, &entrypoint); err != nil || entrypoint == "" {
			return projectSessionConfig{}, errors.New("session: must be a non-empty project path")
		}
		return projectSessionConfig{Entrypoint: entrypoint}, nil
	}
	if firstJSONByte(document) != '{' {
		return projectSessionConfig{}, errors.New("session: must be a project path or an object")
	}
	var config projectSessionConfig
	if err := DecodeJSON(document, &config); err != nil {
		return projectSessionConfig{}, fmt.Errorf("session.%s", projectNestedDecodeMessage(err))
	}
	if config.Entrypoint == "" {
		return projectSessionConfig{}, errors.New("session.entrypoint: is required")
	}
	return config, nil
}

func firstJSONByte(document []byte) byte {
	for _, value := range document {
		if value != ' ' && value != '\t' && value != '\r' && value != '\n' {
			return value
		}
	}
	return 0
}

func DecodeJSON(document []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("multiple JSON values are not allowed")
	}
	return nil
}

func projectNestedDecodeMessage(err error) string {
	if match := unknownJSONFieldPattern.FindStringSubmatch(err.Error()); match != nil {
		return match[1] + ": is not supported"
	}
	return "configuration: " + cleanJSONError(err)
}

func projectFieldFromError(fallback string, err error) string {
	field, _, found := strings.Cut(err.Error(), ": ")
	if found && field != "" {
		return field
	}
	return fallback
}

func projectMessageFromError(err error) string {
	_, message, found := strings.Cut(err.Error(), ": ")
	if found {
		return message
	}
	return err.Error()
}

func describeProjectPathError(err error, directory bool) string {
	if os.IsNotExist(err) {
		if directory {
			return "directory does not exist"
		}
		return "file does not exist"
	}
	return err.Error()
}

func projectDecodeError(file string, document []byte, locations map[string]projectJSONLocation, err error) error {
	var manifestError projectManifestError
	if errors.As(err, &manifestError) {
		manifestError.File = filepath.Clean(file)
		return manifestError
	}
	field := ""
	location := projectJSONLocation{Line: 1, Column: 1}
	if syntaxError, ok := err.(*json.SyntaxError); ok {
		location = projectLineColumn(document, syntaxError.Offset-1)
	} else if typeError, ok := err.(*json.UnmarshalTypeError); ok {
		field = typeError.Field
		location = projectLineColumn(document, typeError.Offset-1)
	} else if match := unknownJSONFieldPattern.FindStringSubmatch(err.Error()); match != nil {
		field = match[1]
		for candidate, candidateLocation := range locations {
			if candidate == field || strings.HasSuffix(candidate, "."+field) {
				field = candidate
				location = candidateLocation
				break
			}
		}
	}
	return projectManifestError{File: filepath.Clean(file), Field: field, Location: location, Message: cleanJSONError(err)}
}

func cleanJSONError(err error) string {
	if match := unknownJSONFieldPattern.FindStringSubmatch(err.Error()); match != nil {
		return "is not supported"
	}
	message := err.Error()
	message = jsonByteOffsetPattern.ReplaceAllString(message, "")
	message = strings.TrimPrefix(message, "json: ")
	return message
}

func newProjectManifestError(file string, locations map[string]projectJSONLocation, field, message string) error {
	location, exists := locations[field]
	for parent := field; !exists && parent != ""; {
		parent, _, _ = strings.Cut(parent, ".")
		location, exists = locations[parent]
		if parent == field {
			break
		}
	}
	if !exists {
		location = projectJSONLocation{Line: 1, Column: 1}
	}
	return projectManifestError{File: filepath.Clean(file), Field: field, Location: location, Message: message}
}

func projectJSONLocations(document []byte) (map[string]projectJSONLocation, error) {
	decoder := json.NewDecoder(bytes.NewReader(document))
	locations := map[string]projectJSONLocation{"": {Line: 1, Column: 1}}
	if err := walkProjectJSON(decoder, document, "", locations); err != nil {
		return nil, err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return nil, errors.New("multiple JSON values are not allowed")
	}
	return locations, nil
}

func walkProjectJSON(decoder *json.Decoder, document []byte, path string, locations map[string]projectJSONLocation) error {
	start := decoder.InputOffset()
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		if path != "" {
			if _, exists := locations[path]; !exists {
				locations[path] = projectLineColumn(document, projectValueOffset(document, start))
			}
		}
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyStart := decoder.InputOffset()
			keyToken, keyErr := decoder.Token()
			if keyErr != nil {
				return keyErr
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("object property name must be a string")
			}
			child := key
			if path != "" {
				child = path + "." + key
			}
			location := projectLineColumn(document, projectValueOffset(document, keyStart))
			if _, duplicate := seen[key]; duplicate {
				return projectManifestError{Field: child, Location: location, Message: "is declared more than once"}
			}
			seen[key] = struct{}{}
			locations[child] = location
			if err := walkProjectJSON(decoder, document, child, locations); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		for index := 0; decoder.More(); index++ {
			child := fmt.Sprintf("%s[%d]", path, index)
			if err := walkProjectJSON(decoder, document, child, locations); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	default:
		return errors.New("invalid JSON delimiter")
	}
}

func projectValueOffset(document []byte, offset int64) int64 {
	for offset < int64(len(document)) {
		switch document[offset] {
		case ' ', '\t', '\r', '\n', ',', ':':
			offset++
		default:
			return offset
		}
	}
	return offset
}

func projectLineColumn(document []byte, offset int64) projectJSONLocation {
	if offset < 0 {
		offset = 0
	}
	if offset > int64(len(document)) {
		offset = int64(len(document))
	}
	line, column := 1, 1
	for _, value := range document[:offset] {
		if value == '\n' {
			line, column = line+1, 1
		} else {
			column++
		}
	}
	return projectJSONLocation{Line: line, Column: column}
}

func validateProjectEnvironment(environment map[string]EnvironmentDeclaration) (string, string) {
	names := make([]string, 0, len(environment))
	for name := range environment {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		declaration := environment[name]
		field := "environment." + name
		if !ids.ConfigNamePattern.MatchString(name) || strings.HasPrefix(name, "CANTELOP_") {
			return field, "name must be a non-reserved environment identifier"
		}
		if declaration.Secret && declaration.Default != nil {
			return field + ".default", "is not allowed for a secret"
		}
		if declaration.Default != nil && (len(*declaration.Default) > MaxConfigInputBytes ||
			strings.ContainsAny(*declaration.Default, "\x00\r\n")) {
			return field + ".default", "contains an unsupported environment value"
		}
	}
	return "", ""
}

func cloneProjectEnvironment(environment map[string]EnvironmentDeclaration) map[string]EnvironmentDeclaration {
	if len(environment) == 0 {
		return nil
	}
	cloned := make(map[string]EnvironmentDeclaration, len(environment))
	for name, declaration := range environment {
		cloned[name] = declaration
	}
	return cloned
}

func LocalAppID(slug string) string {
	digest := sha256.Sum256([]byte("cantelop-local-app:" + slug))
	return fmt.Sprintf("app_%x", digest[:16])
}

func PrepareSessionBuild(config Config, temporaryRoot string) (string, string, error) {
	if config.SessionDockerfile != "" {
		return config.SessionDockerfile, config.Root, nil
	}
	directory := filepath.Join(temporaryRoot, "default-session-base")
	if err := os.Mkdir(directory, 0o700); err != nil {
		return "", "", fmt.Errorf("create default session build context: %w", err)
	}
	dockerfile := filepath.Join(directory, "Dockerfile")
	if err := os.WriteFile(dockerfile, []byte(defaultSessionDockerfile), 0o600); err != nil {
		return "", "", fmt.Errorf("write default session Dockerfile: %w", err)
	}
	return dockerfile, directory, nil
}

func resolveProjectPath(root, value string, directory bool) (string, error) {
	if value == "" || filepath.IsAbs(value) {
		return "", errors.New("path must be relative to cantelop.json")
	}
	clean := filepath.Clean(value)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("path must stay within the project directory")
	}
	absolute := filepath.Join(root, clean)
	info, err := os.Lstat(absolute)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || (directory && !info.IsDir()) || (!directory && !info.Mode().IsRegular()) {
		if directory {
			return "", errors.New("path must be a directory and not a symbolic link")
		}
		return "", errors.New("path must be a regular file and not a symbolic link")
	}
	return absolute, nil
}

// Discovery evaluates the SDK compiler, never the customer's App module.
func discoverProjectApp(config Config) (Config, error) {
	directory, err := os.MkdirTemp("", "cantelop-discovery-*")
	if err != nil {
		return Config{}, err
	}
	defer os.RemoveAll(directory)
	program := SDKLoadProgram + `
 const sdk = await loadSDK("buildEdgeApi");
 const result = await sdk.buildEdgeApi({definition:process.argv[1],outdir:process.argv[2],...(process.argv[3] ? {app:process.argv[3]} : {})});
 process.stdout.write(JSON.stringify(result.manifest));
 `
	command := exec.CommandContext(context.Background(), "node", "--input-type=module", "--eval", program, config.Definition, directory, config.AppSelection)
	command.Dir = config.Root
	stderr := boundedio.NewLimitedBuffer(64 << 10)
	command.Stderr = stderr
	document, err := command.Output()
	if err != nil {
		return Config{}, fmt.Errorf("discover App: %s", strings.TrimSpace(stderr.String()))
	}
	var manifest APIArtifactManifest
	if err := DecodeJSON(document, &manifest); err != nil {
		return Config{}, fmt.Errorf("invalid compiled App: %w", err)
	}
	if _, _, err := manifest.DeclaredRoutes(); err != nil {
		return Config{}, err
	}
	config.AppSlug = manifest.AppName
	config.AppSelection = manifest.AppName
	config.RuntimeID = manifest.SessionRuntimeID
	config.Environment = manifest.Environment
	if manifest.Dockerfile != "" {
		config.SessionDockerfile, err = resolveProjectPath(config.Root, manifest.Dockerfile, false)
		if err != nil {
			return Config{}, err
		}
	}
	return config, nil
}

func BuildDefinition(config Config) string {
	if config.Definition != "" {
		return config.Definition
	}
	return config.APIEntrypoint
}

func BuildApp(config Config) string {
	if config.Definition != "" {
		return config.AppSlug
	}
	return config.SessionEntrypoint
}
