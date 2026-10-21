package config

import (
	"path/filepath"
	"testing"
)

// The operator docs install config.example.json as the starting host
// configuration, so it must stay loadable as the schema evolves.
func TestExampleConfigLoads(t *testing.T) {
	if _, err := Load(filepath.Join("..", "..", "config.example.json")); err != nil {
		t.Fatalf("Load(config.example.json): %v", err)
	}
}
