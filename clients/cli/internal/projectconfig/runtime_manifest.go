package projectconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/stepandel/cantelop/clients/cli/internal/boundedio"
)

type SessionRuntimeManifest struct {
	SchemaVersion              int                               `json:"schema_version"`
	Kind                       string                            `json:"kind"`
	MainModule                 string                            `json:"main_module"`
	CLIBuildProtocolVersion    int                               `json:"cli_build_protocol_version"`
	RuntimeProtocolVersion     int                               `json:"runtime_protocol_version"`
	IntegrationProtocolVersion int                               `json:"integration_protocol_version"`
	SessionRuntimeID           string                            `json:"session_runtime_id"`
	AppName                    string                            `json:"app_name"`
	Environment                map[string]EnvironmentDeclaration `json:"environment,omitempty"`
	Dockerfile                 string                            `json:"dockerfile,omitempty"`
	Capabilities               struct {
		Steering            bool  `json:"steering"`
		MessageCancellation bool  `json:"messageCancellation"`
		DurableView         bool  `json:"durableView"`
		AuthTerminal        *bool `json:"authTerminal,omitempty"`
	} `json:"capabilities"`
	DatabaseSchema json.RawMessage `json:"database_schema,omitempty"`
}

func ValidateRuntimePair(edge APIArtifactManifest, directory string) error {
	document, err := boundedio.ReadRegularFile(filepath.Join(directory, "cantelop-runtime.json"), 2<<20)
	if err != nil {
		return err
	}
	var runtime SessionRuntimeManifest
	if err := DecodeJSON(document, &runtime); err != nil {
		return err
	}
	if runtime.SchemaVersion != 1 || runtime.Kind != "cantelop-session-runtime" || runtime.MainModule != "session-runtime.mjs" || runtime.CLIBuildProtocolVersion != 6 || runtime.RuntimeProtocolVersion != 2 || runtime.IntegrationProtocolVersion != 2 || runtime.AppName != edge.AppName || runtime.SessionRuntimeID != edge.SessionRuntimeID || runtime.Dockerfile != edge.Dockerfile {
		return errors.New("Edge and Session runtime artifacts are incompatible")
	}
	if runtime.Capabilities.AuthTerminal != nil && !*runtime.Capabilities.AuthTerminal ||
		(runtime.Capabilities.AuthTerminal != nil) != (edge.Auth != nil && *edge.Auth) {
		return errors.New("Edge and runtime auth declarations differ")
	}
	left, _ := json.Marshal(runtime.Environment)
	right, _ := json.Marshal(edge.Environment)
	if !bytes.Equal(left, right) {
		return errors.New("Edge and runtime environments differ")
	}
	// Keep the verified runtime manifest beside the Edge for deployment upload.
	return os.WriteFile(filepath.Join(filepath.Dir(directory), "api", "cantelop-runtime.json"), document, 0o600)
}
