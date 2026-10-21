package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stepandel/cantelop/clients/cli/internal/projectconfig"
)

func projectTestRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"api.ts", "session.ts"} {
		if err := os.WriteFile(filepath.Join(root, "src", name), []byte("export {}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func writeProjectManifest(t *testing.T, root, document string) string {
	t.Helper()
	path := filepath.Join(root, projectconfig.DefaultPath)
	if err := os.WriteFile(path, []byte(document), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
