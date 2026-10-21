package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/stepandel/cantelop/clients/cli/internal/controlapi"
	"github.com/stepandel/cantelop/clients/cli/internal/ids"
	"github.com/stepandel/cantelop/clients/cli/internal/projectconfig"
)

// Validate the complete batch before sending any mutations. The control API
// applies each value separately, so an interrupted batch can be retried.
func runConfigSet(ctx context.Context, kind string, args []string, path string, input io.Reader, output io.Writer, session *controlapi.Session) error {
	usage := errors.New("usage: cantelop app env set APP_ID NAME=VALUE [NAME=VALUE ...] | cantelop app secret set APP_ID NAME | cantelop app env|secret set APP_ID --env-file FILE")
	values := make(map[string]string)
	if len(args) == 2 && (args[0] == "--env-file" || args[0] == "-env-file") {
		var err error
		values, err = projectconfig.LoadLocalEnvironment(".", args[1], true)
		if err != nil {
			return err
		}
	} else if kind == "env" {
		for _, arg := range args {
			name, value, found := strings.Cut(arg, "=")
			if !found || !ids.ConfigNamePattern.MatchString(name) {
				return usage
			}
			if _, exists := values[name]; exists {
				return fmt.Errorf("duplicate configuration name %s", name)
			}
			values[name] = value
		}
	} else {
		if len(args) != 1 || !ids.ConfigNamePattern.MatchString(args[0]) || input == nil {
			return usage
		}
		document, err := readCommandInput(ctx, func() ([]byte, error) {
			return io.ReadAll(io.LimitReader(input, projectconfig.MaxConfigInputBytes+2))
		})
		if err != nil {
			return fmt.Errorf("read secret input: %w", err)
		}
		if len(document) > projectconfig.MaxConfigInputBytes+1 {
			return errors.New("secret input exceeds 64 KiB")
		}
		document = bytes.TrimSuffix(document, []byte("\n"))
		document = bytes.TrimSuffix(document, []byte("\r"))
		values[args[0]] = string(document)
	}
	if len(values) == 0 {
		return errors.New("no configuration values supplied")
	}
	updates := make([]configUpdate, 0, len(values))
	expectedKind := "variable"
	if kind == "secret" {
		expectedKind = "secret"
	}
	for name, value := range values {
		updates = append(updates, configUpdate{name: name, value: value, kind: expectedKind})
	}
	if err := validateConfigUpdates(updates); err != nil {
		return err
	}
	return applyConfigUpdates(ctx, updates, strings.TrimSuffix(path, "/"+expectedKind+"s"), output, session)
}

type configUpdate struct{ name, value, kind string }

func validateConfigUpdates(updates []configUpdate) error {
	for _, update := range updates {
		if !ids.ConfigNamePattern.MatchString(update.name) || strings.HasPrefix(update.name, "CANTELOP_") {
			return fmt.Errorf("configuration name %s is invalid or reserved", update.name)
		}
		if len(update.value) > projectconfig.MaxConfigInputBytes {
			return fmt.Errorf("configuration value for %s exceeds 64 KiB", update.name)
		}
		if !utf8.ValidString(update.value) {
			return fmt.Errorf("configuration value for %s is not valid UTF-8", update.name)
		}
	}
	return nil
}

func applyConfigUpdates(ctx context.Context, updates []configUpdate, basePath string, output io.Writer, session *controlapi.Session) error {
	sort.Slice(updates, func(i, j int) bool { return updates[i].name < updates[j].name })
	for i, update := range updates {
		name, expectedKind := update.name, update.kind

		document, err := json.Marshal(map[string]string{"value": update.value})
		if err != nil {
			return err
		}
		var response controlapi.ConfigValue
		err = session.Request(ctx, http.MethodPut, basePath+"/"+expectedKind+"s/"+name, "application/json", bytes.NewReader(document), &response)
		if err == nil && (response.Name != name || response.Kind != expectedKind || (expectedKind == "secret" && response.Value != nil)) {
			err = errors.New("control plane returned an invalid configuration response")
		}
		if err != nil {
			return fmt.Errorf("set configuration %s after %d of %d confirmed updates (the failed update may have applied): %w", name, i, len(updates), err)
		}
		_, _ = fmt.Fprintf(output, "Set %s\n", name)
	}
	return nil
}
