package projectconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/stepandel/cantelop/clients/cli/internal/ids"
)

const maxLocalEnvironmentBytes = 1 << 20

func LoadLocalEnvironment(root, configuredPath string, explicitlySet bool) (map[string]string, error) {
	path := configuredPath
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	document, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) && !explicitlySet {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read local environment file: %w", err)
	}
	if len(document) > maxLocalEnvironmentBytes {
		return nil, errors.New("local environment file exceeds 1 MiB")
	}
	values := make(map[string]string)
	for lineNumber, line := range strings.Split(string(document), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		name, value, found := strings.Cut(line, "=")
		name = strings.TrimSpace(name)
		value = strings.TrimSpace(value)
		if !found || !ids.ConfigNamePattern.MatchString(name) || strings.HasPrefix(name, "CANTELOP_") {
			return nil, fmt.Errorf("local environment line %d is invalid", lineNumber+1)
		}
		if _, exists := values[name]; exists {
			return nil, fmt.Errorf("local environment line %d repeats %s", lineNumber+1, name)
		}
		if strings.HasPrefix(value, "'") != strings.HasSuffix(value, "'") ||
			strings.HasPrefix(value, "\"") != strings.HasSuffix(value, "\"") || value == "'" || value == "\"" {
			return nil, fmt.Errorf("local environment line %d has invalid quoting", lineNumber+1)
		}
		if len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'' {
			value = value[1 : len(value)-1]
		} else if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
			unquoted, err := strconv.Unquote(value)
			if err != nil {
				return nil, fmt.Errorf("local environment line %d has invalid quoting", lineNumber+1)
			}
			value = unquoted
		}
		if strings.ContainsAny(value, "\r\n\x00") {
			return nil, fmt.Errorf("local environment line %d contains an unsupported value", lineNumber+1)
		}
		values[name] = value
	}
	return values, nil
}
