package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/stepandel/cantelop/clients/cli/internal/ids"
	"github.com/stepandel/cantelop/clients/cli/internal/projectconfig"
)

type projectInitDocument struct {
	SchemaVersion int                               `json:"schema_version,omitempty"`
	Definition    string                            `json:"definition,omitempty"`
	App           string                            `json:"app,omitempty"`
	API           string                            `json:"api,omitempty"`
	Session       string                            `json:"session,omitempty"`
	Environment   map[string]projectInitEnvironment `json:"environment,omitempty"`
}

type projectInitEnvironment struct {
	Secret   bool `json:"secret"`
	Required bool `json:"required"`
}

func runProjectInit(args []string, output io.Writer) error {
	const usage = "usage: cantelop init -app APP_SLUG -provider openai|claude|pi [-config cantelop.json] [-definition src/cantelop.ts]"
	flags := flag.NewFlagSet("cantelop init", flag.ContinueOnError)
	app := flags.String("app", "", "existing Cantelop App slug")
	configPath := flags.String("config", projectconfig.DefaultPath, "project configuration path")
	api := flags.String("definition", "src/cantelop.ts", "App definition relative to the manifest")
	provider := flags.String("provider", "", "starter agent provider: openai, claude, or pi")
	if err := parseFlags(flags, args, usage); err != nil {
		return err
	}
	if !ids.ValidRoutingSlug(*app) ||
		!validProjectInitPath(*api) || filepath.Base(*api) == "agent.ts" ||
		strings.TrimSpace(*configPath) == "" || !isProjectInitTemplate(*provider) {
		return errors.New(usage)
	}
	absolute, err := filepath.Abs(*configPath)
	if err != nil {
		return fmt.Errorf("resolve project configuration: %w", err)
	}
	manifest := projectInitDocument{
		SchemaVersion: 3, Definition: *api,
	}
	document, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode project configuration: %w", err)
	}
	root := filepath.Dir(absolute)
	envDocument, _ := json.Marshal(projectInitTemplateEnvironment(*provider))
	definition := strings.ReplaceAll(strings.ReplaceAll(projectInitAPITemplate, "__APP_SLUG__", *app), "__ENVIRONMENT__", string(envDocument))
	files := []projectInitFile{
		{path: absolute, content: append(document, '\n')},
		{path: filepath.Join(root, filepath.FromSlash(*api)), content: []byte(definition)},
		{path: filepath.Join(root, filepath.Dir(filepath.FromSlash(*api)), "agent.ts"), content: []byte(projectInitProviderTemplates[*provider])},
		{path: filepath.Join(root, "package.json"), content: projectInitPackage(*app, *provider)},
	}
	if err := createProjectInitFiles(files); err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "Created %s starter in %s for App %s\nRun npm install @cantelop/sdk, then cantelop dev.\n",
		*provider, filepath.Clean(filepath.Dir(*configPath)), *app)
	return err
}

type projectInitFile struct {
	path    string
	content []byte
}

func createProjectInitFiles(files []projectInitFile) error {
	for _, candidate := range files {
		if _, err := os.Lstat(candidate.path); err == nil {
			return fmt.Errorf("%s already exists; refusing to overwrite it", filepath.Clean(candidate.path))
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect starter target: %w", err)
		}
	}
	createdFiles := make([]string, 0, len(files))
	createdDirectories := make([]string, 0)
	keep := false
	defer func() {
		if keep {
			return
		}
		for index := len(createdFiles) - 1; index >= 0; index-- {
			_ = os.Remove(createdFiles[index])
		}
		for index := len(createdDirectories) - 1; index >= 0; index-- {
			_ = os.Remove(createdDirectories[index])
		}
	}()
	for _, candidate := range files {
		if err := makeProjectInitDirectories(filepath.Dir(candidate.path), &createdDirectories); err != nil {
			return err
		}
		file, err := os.OpenFile(candidate.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			if os.IsExist(err) {
				return fmt.Errorf("%s already exists; refusing to overwrite it", filepath.Clean(candidate.path))
			}
			return fmt.Errorf("create starter file: %w", err)
		}
		createdFiles = append(createdFiles, candidate.path)
		if _, err := file.Write(candidate.content); err != nil {
			_ = file.Close()
			return fmt.Errorf("write starter file: %w", err)
		}
		if err := file.Close(); err != nil {
			return fmt.Errorf("close starter file: %w", err)
		}
	}
	keep = true
	return nil
}

func makeProjectInitDirectories(directory string, created *[]string) error {
	missing := make([]string, 0)
	for current := directory; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err == nil {
			if !info.IsDir() {
				return fmt.Errorf("starter parent %s is not a directory", filepath.Clean(current))
			}
			break
		}
		if !os.IsNotExist(err) {
			return fmt.Errorf("inspect starter parent: %w", err)
		}
		missing = append(missing, current)
		parent := filepath.Dir(current)
		if parent == current {
			return fmt.Errorf("resolve starter parent %s", filepath.Clean(directory))
		}
	}
	for index := len(missing) - 1; index >= 0; index-- {
		if err := os.Mkdir(missing[index], 0o755); err != nil {
			return fmt.Errorf("create starter directory: %w", err)
		}
		*created = append(*created, missing[index])
	}
	return nil
}

func projectInitPackage(app, template string) []byte {
	// @cantelop/sdk is absent on purpose: the first npm install adds the current
	// release and its own caret range, so no SDK version is compiled into the CLI.
	dependencies := map[string]string{}
	switch template {
	case "openai":
		dependencies["@openai/codex-sdk"] = "^0.146.0"
	case "claude":
		dependencies["@anthropic-ai/claude-agent-sdk"] = "^0.3.0"
	case "pi":
		dependencies["@earendil-works/pi-coding-agent"] = "^0.83.0"
	}
	document, _ := json.MarshalIndent(struct {
		Name         string            `json:"name"`
		Private      bool              `json:"private"`
		Type         string            `json:"type"`
		Engines      map[string]string `json:"engines"`
		Dependencies map[string]string `json:"dependencies"`
	}{
		Name: "cantelop-" + app, Private: true, Type: "module",
		Engines: map[string]string{"node": ">=22"}, Dependencies: dependencies,
	}, "", "  ")
	return append(document, '\n')
}

func isProjectInitTemplate(value string) bool {
	_, ok := projectInitProviderTemplates[value]
	return ok
}

func projectInitTemplateEnvironment(template string) map[string]projectInitEnvironment {
	environment := map[string]projectInitEnvironment{}
	switch template {
	case "openai":
		environment["CODEX_API_KEY"] = projectInitEnvironment{Secret: true, Required: true}
	case "claude":
		environment["ANTHROPIC_API_KEY"] = projectInitEnvironment{Secret: true, Required: true}
	}
	return environment
}

func validProjectInitPath(value string) bool {
	if value == "" || filepath.IsAbs(value) {
		return false
	}
	clean := filepath.Clean(value)
	return clean != "." && clean != ".." && !strings.HasPrefix(clean, ".."+string(filepath.Separator))
}
