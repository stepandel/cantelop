package systemd_test

import (
	"bufio"
	"os"
	"strings"
	"testing"
)

func TestServiceUnitPreservesRuntimeSafetyBoundaries(t *testing.T) {
	unit := readUnit(t, "compute/fire-fuse.service")
	want := map[string][]string{
		"Unit.Requires": {"wg-quick@wg0.service"},
		"Service.ExecStart": {
			"/usr/local/bin/fire-fuse -config /etc/fire-fuse/config.json -operator-token-file /etc/fire-fuse/control-clients/operator.token -gateway-token-file /etc/fire-fuse/control-clients/gateway.token -dispatcher-token-file /etc/fire-fuse/control-clients/dispatcher.token -observer-token-file /etc/fire-fuse/control-clients/observer.token -compute-token-file /etc/fire-fuse/control-clients/compute.token -listen 127.0.0.1:8080 -provider-ingress-listen 10.77.0.1:8084 -image-directory /var/lib/fire-fuse/images",
		},
		"Service.User":                   {"root"},
		"Service.EnvironmentFile":        {"/etc/fire-fuse/artifact-store.env"},
		"Service.Restart":                {"on-failure"},
		"Service.KillMode":               {"mixed"},
		"Service.SendSIGKILL":            {"yes"},
		"Service.TimeoutStopSec":         {"2min"},
		"Service.ConfigurationDirectory": {"fire-fuse"},
		"Service.StateDirectory":         {"fire-fuse/state fire-fuse/runtime"},
		"Service.StateDirectoryMode":     {"0750"},
		"Service.ProtectSystem":          {"strict"},
		"Service.PrivateDevices":         {"no"},
		"Service.PrivateUsers":           {"no"},
		"Service.RestrictNamespaces":     {"no"},
		"Service.ProtectControlGroups":   {"no"},
		"Service.DevicePolicy":           {"closed"},
		"Service.DeviceAllow":            {"/dev/kvm rwm", "/dev/net/tun rwm", "/dev/userfaultfd rwm"},
		"Service.ReadWritePaths":         {"/var/lib/fire-fuse/images -/var/lib/fire-fuse/workspaces -/var/lib/fire-fuse/workspace-admin /var/lib/nfs /sys/fs/cgroup"},
		"Service.NoNewPrivileges":        {"no"},
	}
	for key, values := range want {
		got := unit[key]
		if len(got) != len(values) {
			t.Errorf("%s values = %q, want %q", key, got, values)
			continue
		}
		for index := range values {
			if got[index] != values[index] {
				t.Errorf("%s[%d] = %q, want %q", key, index, got[index], values[index])
			}
		}
	}
	for _, key := range []string{
		"Service.CapabilityBoundingSet",
		"Service.SystemCallFilter",
	} {
		if values := unit[key]; len(values) != 0 {
			t.Errorf("%s = %q, want unset until real-host validation", key, values)
		}
	}
}

func TestGatewayUnitRunsUnprivilegedOnLoopback(t *testing.T) {
	unit := readUnit(t, "compute/fire-fuse-gateway.service")
	want := map[string][]string{
		"Unit.Requires": {"fire-fuse.service"},
		"Unit.PartOf":   {"fire-fuse.service"},
		"Service.ExecStart": {
			"/usr/local/bin/fire-fuse-gateway -listen 127.0.0.1:8081 -control-plane http://127.0.0.1:8080 -product-control https://console.cantelop.dev -control-timeout 45s -control-token-file /etc/fire-fuse-gateway/runtime-control.token -product-token-file /etc/fire-fuse-gateway/product-control.token -edge-token-file /etc/fire-fuse-gateway/edge-ingress.token",
		},
		"Service.User":                    {"fire-fuse-gateway"},
		"Service.Group":                   {"fire-fuse-gateway"},
		"Service.Restart":                 {"on-failure"},
		"Service.NoNewPrivileges":         {"yes"},
		"Service.PrivateDevices":          {"yes"},
		"Service.ProtectSystem":           {"strict"},
		"Service.ProtectHome":             {"yes"},
		"Service.ProtectKernelTunables":   {"yes"},
		"Service.ProtectControlGroups":    {"yes"},
		"Service.RestrictAddressFamilies": {"AF_INET AF_INET6"},
	}
	if values := unit["Service.SupplementaryGroups"]; len(values) != 0 {
		t.Fatalf("gateway supplementary groups = %q", values)
	}
	for key, values := range want {
		got := unit[key]
		if len(got) != len(values) {
			t.Errorf("%s values = %q, want %q", key, got, values)
			continue
		}
		for index := range values {
			if got[index] != values[index] {
				t.Errorf("%s[%d] = %q, want %q", key, index, got[index], values[index])
			}
		}
	}
}

func TestAsyncDispatcherUnitRunsUnprivilegedAgainstLoopbackControlPlane(t *testing.T) {
	unit := readUnit(t, "compute/fire-fuse-async-dispatcher.service")
	want := map[string][]string{
		"Unit.Requires": {"fire-fuse.service"},
		"Unit.PartOf":   {"fire-fuse.service"},
		"Service.ExecStart": {
			"/usr/local/bin/fire-fuse-async-dispatcher -control-plane http://127.0.0.1:8080 -control-timeout 45s -control-token-file /etc/fire-fuse-dispatcher/runtime-control.token -observability-token-file /etc/fire-fuse-dispatcher/observability-ingest.token -observability-spool /var/lib/fire-fuse-observability -observability-spool-max-bytes 67108864 -observability-spool-max-age 24h",
		},
		"Service.User":                    {"fire-fuse-dispatcher"},
		"Service.Group":                   {"fire-fuse-dispatcher"},
		"Service.Restart":                 {"on-failure"},
		"Service.NoNewPrivileges":         {"yes"},
		"Service.PrivateDevices":          {"yes"},
		"Service.ProtectSystem":           {"strict"},
		"Service.ProtectHome":             {"yes"},
		"Service.ProtectKernelTunables":   {"yes"},
		"Service.ProtectControlGroups":    {"yes"},
		"Service.RestrictAddressFamilies": {"AF_INET AF_INET6"},
		"Service.StateDirectory":          {"fire-fuse-observability"},
		"Service.StateDirectoryMode":      {"0700"},
		"Service.ReadWritePaths":          {"/var/lib/fire-fuse-observability"},
	}
	if values := unit["Service.SupplementaryGroups"]; len(values) != 0 {
		t.Fatalf("dispatcher supplementary groups = %q", values)
	}
	for key, values := range want {
		got := unit[key]
		if len(got) != len(values) {
			t.Errorf("%s values = %q, want %q", key, got, values)
			continue
		}
		for index := range values {
			if got[index] != values[index] {
				t.Errorf("%s[%d] = %q, want %q", key, index, got[index], values[index])
			}
		}
	}
}

func TestObserverUnitReadsOnlyLoopbackServices(t *testing.T) {
	unit := readUnit(t, "compute/fire-fuse-observer.service")
	want := map[string][]string{
		"Unit.Requires": {"fire-fuse.service"},
		"Unit.PartOf":   {"fire-fuse.service"},
		"Service.ExecStart": {
			"/usr/local/bin/fire-fuse-observer -listen 127.0.0.1:8082 -control-plane http://127.0.0.1:8080 -netdata http://127.0.0.1:19999 -control-token-file /etc/fire-fuse-observer/runtime-control.token -token-file /etc/fire-fuse-observer/observer.token -mutation-token-file /etc/fire-fuse-observer/session-mutation.token",
		},
		"Service.User":                    {"fire-fuse-observer"},
		"Service.Group":                   {"fire-fuse-observer"},
		"Service.Restart":                 {"on-failure"},
		"Service.NoNewPrivileges":         {"yes"},
		"Service.PrivateDevices":          {"yes"},
		"Service.ProtectSystem":           {"strict"},
		"Service.ProtectHome":             {"yes"},
		"Service.ProtectKernelTunables":   {"yes"},
		"Service.ProtectControlGroups":    {"yes"},
		"Service.RestrictAddressFamilies": {"AF_INET AF_INET6"},
	}
	if values := unit["Service.SupplementaryGroups"]; len(values) != 0 {
		t.Fatalf("observer supplementary groups = %q", values)
	}
	for key, values := range want {
		got := unit[key]
		if len(got) != len(values) {
			t.Errorf("%s values = %q, want %q", key, got, values)
			continue
		}
		for index := range values {
			if got[index] != values[index] {
				t.Errorf("%s[%d] = %q, want %q", key, index, got[index], values[index])
			}
		}
	}
}

func TestDependentUnitsRestartWithFireFuse(t *testing.T) {
	for _, name := range []string{
		"fire-fuse-async-dispatcher.service",
		"fire-fuse-gateway.service",
		"fire-fuse-observer.service",
	} {
		t.Run(name, func(t *testing.T) {
			unit := readUnit(t, "compute/"+name)
			for _, key := range []string{"Unit.Requires", "Unit.PartOf"} {
				values := unit[key]
				if len(values) != 1 || values[0] != "fire-fuse.service" {
					t.Fatalf("%s = %q, want [fire-fuse.service]", key, values)
				}
			}
		})
	}
}

func TestBuilderPrivilegeBoundary(t *testing.T) {
	worker := readUnit(t, "builder/cantelop-control-worker.service")
	builder := readUnit(t, "builder/cantelop-build-materializer.service")
	if got := worker["Service.NoNewPrivileges"]; len(got) != 1 || got[0] != "yes" {
		t.Fatalf("control worker privilege boundary = %q", got)
	}
	if got := worker["Unit.Requires"]; len(got) != 1 || got[0] != "wg-quick@wg0.service cantelop-build-materializer.service" {
		t.Fatalf("control worker dependencies = %q", got)
	}
	if got := builder["Service.User"]; len(got) != 1 || got[0] != "root" {
		t.Fatalf("converter user = %q", got)
	}
	if got := builder["Service.RestrictAddressFamilies"]; len(got) != 1 || got[0] != "AF_UNIX" {
		t.Fatalf("converter network boundary = %q", got)
	}
	if got := builder["Service.ReadWritePaths"]; len(got) != 1 || strings.Contains(got[0], "/var/lib/fire-fuse") {
		t.Fatalf("converter writable paths = %q", got)
	}
}

func readUnit(t *testing.T, path string) map[string][]string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open service unit: %v", err)
	}
	defer file.Close()

	values := make(map[string][]string)
	section := ""
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSuffix(strings.TrimPrefix(line, "["), "]")
			continue
		}
		name, value, found := strings.Cut(line, "=")
		if !found || section == "" || strings.TrimSpace(name) == "" {
			t.Fatalf("invalid unit line %q", line)
		}
		key := section + "." + strings.TrimSpace(name)
		values[key] = append(values[key], strings.TrimSpace(value))
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read service unit: %v", err)
	}
	return values
}
