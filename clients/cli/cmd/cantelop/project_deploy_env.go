package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/stepandel/cantelop/clients/cli/internal/projectconfig"
)

// The manifest is the allowlist and determines how each supplied value is stored.
// Defaults remain local-development defaults, even for required declarations.
func loadProjectDeploymentEnvironment(config projectconfig.Config, path string) ([]configUpdate, error) {
	values, err := projectconfig.LoadLocalEnvironment(config.Root, path, true)
	if err != nil {
		return nil, err
	}
	var updates []configUpdate
	var missing []string
	for name, declaration := range config.Environment {
		value, exists := values[name]
		if !exists {
			if declaration.Required {
				missing = append(missing, name)
			}
			continue
		}
		kind := "variable"
		if declaration.Secret {
			kind = "secret"
		}
		updates = append(updates, configUpdate{name: name, value: value, kind: kind})
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("environment file is missing required variables: %s", strings.Join(missing, ", "))
	}
	sort.Slice(updates, func(i, j int) bool { return updates[i].name < updates[j].name })
	if err := validateConfigUpdates(updates); err != nil {
		return nil, err
	}
	return updates, nil
}
