package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeploymentRetryIdentitySurvivesProcessRetry(t *testing.T) {
	t.Setenv("CANTELOP_CONFIG", filepath.Join(t.TempDir(), "credentials.json"))
	appID := "app_0123456789abcdef0123456789abcdef"
	apiDigest := "sha256:" + strings.Repeat("a", 64)
	sessionRuntimeDigest := "sha256:" + strings.Repeat("b", 64)

	first, path, err := RetryIdentity(appID, apiDigest, sessionRuntimeDigest)
	if err != nil {
		t.Fatal(err)
	}
	second, secondPath, err := RetryIdentity(appID, apiDigest, sessionRuntimeDigest)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || path != secondPath {
		t.Fatalf("retry identities = %q/%q at %q/%q", first, second, path, secondPath)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("retry state mode = %v, %v", info.Mode().Perm(), err)
	}
}
