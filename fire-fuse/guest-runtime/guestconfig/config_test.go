package guestconfig

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestDecode(t *testing.T) {
	document := `{
		"schema_version": 1,
		"process": {
			"args": ["/docker-entrypoint.sh", "serve"],
			"env": ["PATH=/usr/bin:/bin", "PORT=8080"],
			"working_directory": "/app",
			"uid": 1000,
			"gid": 1000
		},
		"workspace": {
			"enabled": true,
			"container_path": "/workspace"
		}
	}`

	config, err := Decode(strings.NewReader(document))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if config.SchemaVersion != SchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", config.SchemaVersion, SchemaVersion)
	}
	if got := config.Process.Args; len(got) != 2 || got[0] != "/docker-entrypoint.sh" || got[1] != "serve" {
		t.Errorf("Process.Args = %#v", got)
	}
	if config.Process.UID != 1000 || config.Process.GID != 1000 {
		t.Errorf("process identity = %d:%d, want 1000:1000", config.Process.UID, config.Process.GID)
	}
	if !config.Workspace.Enabled || config.Workspace.ContainerPath != "/workspace" {
		t.Errorf("Workspace = %#v", config.Workspace)
	}
}

func TestMergeEnvironmentOverridesImageDefaults(t *testing.T) {
	merged, err := MergeEnvironment(
		[]string{"PATH=/usr/bin", "PORT=80"},
		[]string{"PORT=3000", "TOKEN=secret"},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"PATH=/usr/bin", "PORT=3000", "TOKEN=secret"}
	if !reflect.DeepEqual(merged, want) {
		t.Fatalf("MergeEnvironment() = %#v, want %#v", merged, want)
	}
	if _, err := MergeEnvironment(nil, []string{"TOKEN=one", "TOKEN=two"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate override error = %v, want ErrInvalid", err)
	}
}

func TestSDKListenerPort(t *testing.T) {
	port, ok, err := SDKListenerPort([]string{"PATH=/bin", "CANTELOP_INTERNAL_PORT=8080"})
	if err != nil || !ok || port != 8080 {
		t.Fatalf("SDKListenerPort() = %d, %t, %v", port, ok, err)
	}
	if _, ok, err := SDKListenerPort([]string{"PATH=/bin"}); err != nil || ok {
		t.Fatalf("SDKListenerPort() without SDK port = _, %t, %v", ok, err)
	}
	for _, value := range []string{"0", "65536", "invalid"} {
		if _, _, err := SDKListenerPort([]string{"CANTELOP_INTERNAL_PORT=" + value}); !errors.Is(err, ErrInvalid) {
			t.Errorf("SDKListenerPort(%q) error = %v, want ErrInvalid", value, err)
		}
	}
}

func TestPlatformRuntimeRequiresOneBundledSessionRuntime(t *testing.T) {
	config := validConfig()
	config.Process.Args = []string{PlatformRuntimeContainerPath}
	if err := config.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Validate() error = %v, want ErrInvalid", err)
	}
	config.Process.Args = []string{PlatformRuntimeContainerPath, "/opt/cantelop/session-runtime.mjs"}
	if err := config.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestDecodeRejectsInvalidDocuments(t *testing.T) {
	tests := []struct {
		name     string
		document string
	}{
		{name: "nil reader"},
		{name: "empty document", document: ""},
		{name: "unknown field", document: validDocument(`"unknown": true,`)},
		{name: "multiple documents", document: validDocument("") + `{}`},
		{name: "unsupported version", document: strings.Replace(validDocument(""), `"schema_version": 1`, `"schema_version": 2`, 1)},
		{name: "missing args", document: strings.Replace(validDocument(""), `"args": ["/bin/server"],`, `"args": [],`, 1)},
		{name: "empty executable", document: strings.Replace(validDocument(""), `["/bin/server"]`, `[""]`, 1)},
		{name: "argument NUL", document: strings.Replace(validDocument(""), `["/bin/server"]`, `["/bin/ser\u0000ver"]`, 1)},
		{name: "environment without equals", document: strings.Replace(validDocument(""), `["PATH=/bin"]`, `["PATH"]`, 1)},
		{name: "empty environment name", document: strings.Replace(validDocument(""), `["PATH=/bin"]`, `["=/bin"]`, 1)},
		{name: "duplicate environment", document: strings.Replace(validDocument(""), `["PATH=/bin"]`, `["PATH=/bin", "PATH=/usr/bin"]`, 1)},
		{name: "relative working directory", document: strings.Replace(validDocument(""), `"/app"`, `"app"`, 1)},
		{name: "unclean working directory", document: strings.Replace(validDocument(""), `"/app"`, `"/srv/../app"`, 1)},
		{name: "root uid", document: strings.Replace(validDocument(""), `"uid": 1000`, `"uid": 0`, 1)},
		{name: "root primary gid", document: strings.Replace(validDocument(""), `"gid": 1000`, `"gid": 0`, 1)},
		{name: "enabled workspace without path", document: strings.Replace(validDocument(""), `"/workspace"`, `""`, 1)},
		{name: "relative workspace path", document: strings.Replace(validDocument(""), `"/workspace"`, `"workspace"`, 1)},
		{name: "workspace at root", document: strings.Replace(validDocument(""), `"/workspace"`, `"/"`, 1)},
		{name: "disabled workspace with path", document: strings.Replace(validDocument(""), `"enabled": true`, `"enabled": false`, 1)},
		{name: "oversized document", document: strings.Repeat(" ", maxDocumentBytes+1)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var err error
			if test.name == "nil reader" {
				_, err = Decode(nil)
			} else {
				_, err = Decode(strings.NewReader(test.document))
			}
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("Decode() error = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestValidateBoundsCollectionsAndStrings(t *testing.T) {
	config := validConfig()
	config.Process.Args = make([]string, maxArguments+1)
	if err := config.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Validate() oversized args error = %v, want ErrInvalid", err)
	}

	config = validConfig()
	config.Process.Env = make([]string, maxEnvironment+1)
	if err := config.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Validate() oversized env error = %v, want ErrInvalid", err)
	}

	config = validConfig()
	config.Process.Args[0] = strings.Repeat("x", maxStringBytes+1)
	if err := config.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Validate() oversized string error = %v, want ErrInvalid", err)
	}
}

func validConfig() Config {
	return Config{
		SchemaVersion: SchemaVersion,
		Process: Process{
			Args:             []string{"/bin/server"},
			Env:              []string{"PATH=/bin"},
			WorkingDirectory: "/app",
			UID:              1000,
			GID:              1000,
		},
		Workspace: Workspace{
			Enabled:       true,
			ContainerPath: "/workspace",
		},
	}
}

func validDocument(extra string) string {
	return `{
		` + extra + `
		"schema_version": 1,
		"process": {
			"args": ["/bin/server"],
			"env": ["PATH=/bin"],
			"working_directory": "/app",
			"uid": 1000,
			"gid": 1000
		},
		"workspace": {
			"enabled": true,
			"container_path": "/workspace"
		}
	}`
}
