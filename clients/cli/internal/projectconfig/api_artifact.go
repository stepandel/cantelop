package projectconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/stepandel/cantelop/clients/cli/internal/boundedio"
	"github.com/stepandel/cantelop/clients/cli/internal/ids"
)

const maxAPIModuleBytes = 1 << 20

// APIArtifactManifest is forwarded to the console verbatim. Schema version 2
// has no routes member. Version 3 always has one: the API's route table, or
// null when the SDK build could not discover it.
type APIArtifactManifest struct {
	SchemaVersion                 int                               `json:"schema_version"`
	Kind                          string                            `json:"kind"`
	MainModule                    string                            `json:"main_module"`
	Routes                        json.RawMessage                   `json:"routes,omitempty"`
	DatabaseSchema                json.RawMessage                   `json:"database_schema,omitempty"`
	AppName                       string                            `json:"app_name,omitempty"`
	SessionRuntimeID              string                            `json:"session_runtime_id,omitempty"`
	CLIBuildProtocolVersion       int                               `json:"cli_build_protocol_version,omitempty"`
	IntegrationProtocolVersion    int                               `json:"integration_protocol_version,omitempty"`
	FleetExecutionProtocolVersion int                               `json:"fleet_execution_protocol_version,omitempty"`
	RequiredBindings              []string                          `json:"required_bindings,omitempty"`
	OptionalBindings              []string                          `json:"optional_bindings,omitempty"`
	DefaultKeepAliveBinding       string                            `json:"default_keep_alive_binding,omitempty"`
	Webhooks                      []APIRoute                        `json:"webhooks,omitempty"`
	Homepage                      *bool                             `json:"homepage,omitempty"`
	Auth                          *bool                             `json:"auth,omitempty"`
	Environment                   map[string]EnvironmentDeclaration `json:"environment,omitempty"`
	Dockerfile                    string                            `json:"dockerfile,omitempty"`
}

// validOptionalBindings accepts older manifests and the declared Fleet binding
// only when the Worker advertises the matching Fleet execution contract.
func validOptionalBindings(bindings []string, fleetVersion int) bool {
	rotation := []string{"CANTELOP_INTEGRATION_TOKEN_PREVIOUS", "CANTELOP_INTEGRATION_TOKEN_PREVIOUS_EXPIRES_AT"}
	return bindings == nil || slices.Equal(bindings, rotation) || fleetVersion == 1 && slices.Equal(bindings, append(rotation, "CANTELOP_FLEET_EXECUTION_VERSION"))
}

type APIRoute struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

const maxAPIManifestRoutes = 100

var (
	APIRouteMethods = []string{"DELETE", "GET", "HEAD", "OPTIONS", "PATCH", "POST", "PUT"}
	// The SDK commands the generated Edge Worker serves, each at POST /commands/{type}.
	AppCommandTypes     = []string{"workspace.resolve", "workspace.database", "dispatch", "steer", "request", "auth", "cancel", "status", "view", "stop", "stream"}
	APIRoutePathPattern = regexp.MustCompile(`^/[\x21-\x7e]{0,127}$`)
)

// routes returns the declared route table. discovered is false when the
// manifest carries none: a version 2 manifest, or a version 3 null.
func (manifest APIArtifactManifest) DeclaredRoutes() (routes []APIRoute, discovered bool, err error) {
	if manifest.Kind == "cantelop-protocol-edge" {
		if manifest.FleetExecutionProtocolVersion != 0 && manifest.FleetExecutionProtocolVersion != 1 {
			return nil, false, errors.New("unsupported Fleet execution protocol")
		}
		if manifest.Auth != nil && !*manifest.Auth {
			return nil, false, errors.New("invalid protocol Edge auth declaration")
		}
		if manifest.SchemaVersion != 1 || manifest.MainModule != "worker.mjs" || manifest.CLIBuildProtocolVersion != 6 || manifest.IntegrationProtocolVersion != 2 || !ids.ValidRoutingSlug(manifest.AppName) || !ids.LocalSessionKeyPattern.MatchString(manifest.SessionRuntimeID) || manifest.Homepage == nil || len(manifest.Routes) != 0 || len(manifest.DatabaseSchema) != 0 || len(manifest.RequiredBindings) != 1 || manifest.RequiredBindings[0] != "CANTELOP_INTEGRATION_TOKEN" || !validOptionalBindings(manifest.OptionalBindings, manifest.FleetExecutionProtocolVersion) || manifest.DefaultKeepAliveBinding != "CANTELOP_DEFAULT_KEEP_ALIVE_SECONDS" {
			return nil, false, errors.New("invalid protocol Edge manifest")
		}
		if _, message := validateProjectEnvironment(manifest.Environment); message != "" {
			return nil, false, errors.New(message)
		}
		for _, command := range AppCommandTypes {
			if command != "auth" || manifest.Auth != nil {
				routes = append(routes, APIRoute{Method: "POST", Path: "/commands/" + command})
			}
		}
		if *manifest.Homepage {
			routes = append(routes, APIRoute{Method: "GET", Path: "/"}, APIRoute{Method: "HEAD", Path: "/"})
		}
		seen := map[string]bool{}
		for _, route := range manifest.Webhooks {
			if !strings.HasPrefix(route.Path, "/webhooks/") || !APIRoutePathPattern.MatchString(route.Path) || strings.HasSuffix(route.Path, "/") || (route.Method != "GET" && route.Method != "POST") || seen[route.Path] {
				return nil, false, errors.New("invalid webhook route")
			}
			seen[route.Path] = true
			routes = append(routes, route)
		}
		sort.Slice(routes, func(i, j int) bool {
			if routes[i].Path == routes[j].Path {
				return routes[i].Method < routes[j].Method
			}
			return routes[i].Path < routes[j].Path
		})
		return routes, true, nil
	}
	if manifest.Auth != nil || manifest.AppName != "" || manifest.SessionRuntimeID != "" || manifest.Homepage != nil || manifest.CLIBuildProtocolVersion != 0 || manifest.IntegrationProtocolVersion != 0 || manifest.FleetExecutionProtocolVersion != 0 || manifest.Environment != nil || manifest.Dockerfile != "" || manifest.RequiredBindings != nil || manifest.OptionalBindings != nil || manifest.Webhooks != nil || manifest.DefaultKeepAliveBinding != "" {
		return nil, false, errors.New("unexpected protocol configuration")
	}
	if manifest.SchemaVersion == 4 {
		var schema struct {
			Version  int             `json:"version"`
			Digest   string          `json:"digest"`
			Snapshot json.RawMessage `json:"snapshot"`
		}
		if json.Unmarshal(manifest.DatabaseSchema, &schema) != nil || schema.Version != 1 || !ids.DockerImageIDPattern.MatchString(schema.Digest) || len(schema.Snapshot) == 0 {
			return nil, false, errors.New("invalid database schema")
		}
	} else if len(manifest.DatabaseSchema) != 0 {
		return nil, false, errors.New("unexpected database schema")
	}
	if manifest.SchemaVersion == 2 {
		if len(manifest.Routes) != 0 {
			return nil, false, errors.New("unexpected routes")
		}
		return nil, false, nil
	}
	if (manifest.SchemaVersion != 3 && manifest.SchemaVersion != 4) || len(manifest.Routes) == 0 {
		return nil, false, errors.New("unsupported manifest")
	}
	if string(manifest.Routes) == "null" {
		return nil, false, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(manifest.Routes))
	decoder.DisallowUnknownFields()
	routes = []APIRoute{}
	if decoder.Decode(&routes) != nil || routes == nil || len(routes) > maxAPIManifestRoutes {
		return nil, false, errors.New("invalid routes")
	}
	for index, route := range routes {
		if !slices.Contains(APIRouteMethods, route.Method) || !APIRoutePathPattern.MatchString(route.Path) ||
			(len(route.Path) > 1 && strings.HasSuffix(route.Path, "/")) {
			return nil, false, errors.New("invalid route")
		}
		if index > 0 {
			previous := routes[index-1]
			if previous.Path > route.Path || (previous.Path == route.Path && previous.Method >= route.Method) {
				return nil, false, errors.New("unsorted routes")
			}
		}
	}
	return routes, true, nil
}

type APIArtifact struct {
	Manifest         APIArtifactManifest `json:"manifest"`
	ManifestDocument json.RawMessage     `json:"-"`
	Module           []byte              `json:"module"`
}

func LoadAPIArtifact(directory string) (APIArtifact, error) {
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return APIArtifact{}, err
	}
	info, err := os.Lstat(absolute)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return APIArtifact{}, errors.New("artifact path must be a directory")
	}
	manifestDocument, err := boundedio.ReadRegularFile(filepath.Join(absolute, "cantelop-edge.json"), 2<<20)
	if _, statErr := os.Lstat(filepath.Join(absolute, "cantelop-edge.json")); errors.Is(statErr, os.ErrNotExist) {
		manifestDocument, err = boundedio.ReadRegularFile(filepath.Join(absolute, "cantelop-api.json"), 2<<20)
	}
	if err != nil {
		return APIArtifact{}, fmt.Errorf("read cantelop-api.json: %w", err)
	}
	var manifest APIArtifactManifest
	decoder := json.NewDecoder(bytes.NewReader(manifestDocument))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&manifest) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return APIArtifact{}, errors.New("cantelop-api.json is invalid")
	}
	if !validAPIArtifact(APIArtifact{Manifest: manifest, Module: []byte("validation")}) {
		return APIArtifact{}, errors.New("invalid API artifact")
	}
	module, err := boundedio.ReadRegularFile(filepath.Join(absolute, manifest.MainModule), maxAPIModuleBytes)
	if err != nil {
		return APIArtifact{}, fmt.Errorf("read API module: %w", err)
	}
	artifact := APIArtifact{Manifest: manifest, ManifestDocument: manifestDocument, Module: module}
	if !validAPIArtifact(artifact) {
		return APIArtifact{}, errors.New("invalid API artifact")
	}
	return artifact, nil
}

func validAPIArtifact(artifact APIArtifact) bool {
	_, _, routesErr := artifact.Manifest.DeclaredRoutes()
	return routesErr == nil && (artifact.Manifest.Kind == "cantelop-edge-api" || artifact.Manifest.Kind == "cantelop-protocol-edge") &&
		artifact.Manifest.MainModule == "worker.mjs" &&
		len(artifact.Module) > 0 && len(artifact.Module) <= maxAPIModuleBytes && utf8.Valid(artifact.Module)
}
