package databaseaccess

import (
	"strings"
	"testing"
)

func TestEnvironmentBindsIdentity(t *testing.T) {
	access, err := New("https://console.cantelop.dev/internal/v1/runtime/database/credentials", strings.Repeat("01", 32))
	if err != nil {
		t.Fatal(err)
	}
	env, err := access.Environment(Identity{SandboxID: "sbx-0123456789abcdef0123456789abcdef", AppID: "app_0123456789abcdef0123456789abcdef", WorkspaceID: "wsp_0123456789abcdef0123456789abcdef"})
	if err != nil || len(env) != 2 {
		t.Fatalf("environment: %v, %v", env, err)
	}
	expected := "CANTELOP_WORKSPACE_DATABASE_ACCESS_TOKEN=eyJzYW5kYm94SWQiOiJzYngtMDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWYiLCJhcHBJZCI6ImFwcF8wMTIzNDU2Nzg5YWJjZGVmMDEyMzQ1Njc4OWFiY2RlZiIsIndvcmtzcGFjZUlkIjoid3NwXzAxMjM0NTY3ODlhYmNkZWYwMTIzNDU2Nzg5YWJjZGVmIn0."
	if !strings.HasPrefix(env[1], expected) {
		t.Fatal("capability must bind the assigned sandbox, App and Workspace")
	}
	if _, err := access.Environment(Identity{}); err == nil {
		t.Fatal("invalid identity accepted")
	}
}
func TestConfigurationRequiresHTTPSAndDedicatedKey(t *testing.T) {
	for _, endpoint := range []string{"http://console.cantelop.dev/internal/v1/runtime/database/credentials", "https://user:pass@console.cantelop.dev/internal/v1/runtime/database/credentials", "https://console.cantelop.dev/other"} {
		if _, err := New(endpoint, strings.Repeat("01", 32)); err == nil {
			t.Fatal("invalid endpoint accepted")
		}
	}
}
