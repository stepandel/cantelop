package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/guest-runtime/guestconfig"
)

func TestLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	contents := `{
  "firecracker_bin": "/usr/local/bin/firecracker",
  "jailer_bin": "/usr/local/bin/jailer",
  "jailer_uid": 1000,
  "jailer_gid": 1000,
  "runtime_dir": "/var/lib/fire-fuse",
  "state_db": "/var/lib/fire-fuse/state.db",
  "network": {
    "ip_bin": "/usr/sbin/ip",
    "iptables_bin": "/usr/sbin/iptables",
    "ip6tables_bin": "/usr/sbin/ip6tables",
    "address_pool": "172.16.0.0/16",
    "egress": {
      "dns_resolvers": ["1.1.1.1", "8.8.8.8"],
      "allowed_tcp_ports": [22, 80, 443],
      "allowed_udp_ports": [443]
    }
  },
  "workspace": {
    "exportfs_bin": "/usr/sbin/exportfs",
    "juicefs_bin": "/usr/local/bin/juicefs",
    "mountpoint_bin": "/usr/bin/mountpoint",
    "root": "/var/lib/fire-fuse/workspaces",
    "admin_root": "/var/lib/fire-fuse/workspace-admin"
  },
  "limits": {
    "max_vms": 32,
    "max_total_memory_mib": 32768,
    "max_concurrent_starts": 2
  },
  "timeouts": {
    "startup": "30s",
    "api_socket": "5s",
    "guest_shutdown": "5s",
    "shutdown_grace": "5s",
    "shutdown_force": "5s",
    "cleanup": "5s",
    "http_request_read": "10s",
    "http_idle": "30s",
    "server_shutdown": "5s"
  },
	"image_catalog": "/etc/fire-fuse/image-catalog.json",
  "config_encryption_key_file": "/etc/fire-fuse/config-encryption.key",
  "profiles": {
	"default": {
      "kernel_image": "/var/lib/fire-fuse/images/vmlinux",
      "rootfs_image": "/var/lib/fire-fuse/images/ubuntu.ext4",
      "snapshot_bundle": "/var/lib/fire-fuse/snapshots/ubuntu-24",
      "boot_args": "console=ttyS0 reboot=k panic=1"
    }
  }
}`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.FirecrackerBin != "/usr/local/bin/firecracker" {
		t.Errorf("FirecrackerBin = %q", cfg.FirecrackerBin)
	}
	if cfg.StateDB != "/var/lib/fire-fuse/state.db" {
		t.Errorf("StateDB = %q", cfg.StateDB)
	}
	if cfg.Network.IPBin != "/usr/sbin/ip" ||
		cfg.Network.IPTablesBin != "/usr/sbin/iptables" ||
		cfg.Network.IP6TablesBin != "/usr/sbin/ip6tables" ||
		cfg.Network.AddressPool != "172.16.0.0/16" {
		t.Errorf("Network = %#v", cfg.Network)
	}
	if cfg.Workspace == nil ||
		cfg.Workspace.ExportFSBin != "/usr/sbin/exportfs" ||
		cfg.Workspace.MountpointBin != "/usr/bin/mountpoint" ||
		cfg.Workspace.Root != "/var/lib/fire-fuse/workspaces" ||
		cfg.Workspace.AdminRoot != "/var/lib/fire-fuse/workspace-admin" {
		t.Errorf("Workspace = %#v", cfg.Workspace)
	}
	if cfg.Limits.MaxVMs != 32 || cfg.Limits.MaxConcurrentStarts != 2 {
		t.Errorf("Limits = %#v", cfg.Limits)
	}
	if got := time.Duration(cfg.Timeouts.Startup); got != 30*time.Second {
		t.Errorf("Timeouts.Startup = %s, want 30s", got)
	}
	if got := time.Duration(cfg.Timeouts.GuestShutdown); got != 5*time.Second {
		t.Errorf("Timeouts.GuestShutdown = %s, want 5s", got)
	}
	if cfg.ImageCatalog != "/etc/fire-fuse/image-catalog.json" {
		t.Errorf("ImageCatalog = %q", cfg.ImageCatalog)
	}
	if cfg.Images != nil {
		t.Errorf("Images = %#v, want catalog loaded only by LoadTrusted", cfg.Images)
	}
	profile, err := cfg.GetProfile("default")
	if err != nil {
		t.Fatalf("GetProfile() error = %v", err)
	}
	if profile.RootFSImage != "/var/lib/fire-fuse/images/ubuntu.ext4" {
		t.Errorf("RootFSImage = %q", profile.RootFSImage)
	}
	if profile.SnapshotBundle != "/var/lib/fire-fuse/snapshots/ubuntu-24" {
		t.Errorf("SnapshotBundle = %q", profile.SnapshotBundle)
	}
}

func TestLoadRejectsUnknownField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"unexpected":true}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("Load() error = %v, want unknown field error", err)
	}
}

func TestLoadConfigEncryptionKeyRequiresPrivateLowercaseHex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config-encryption.key")
	if err := os.WriteFile(path, []byte(strings.Repeat("ab", 32)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	checked := false
	key, err := loadConfigEncryptionKey(path, func(field, gotPath string) error {
		checked = true
		if field != "config_encryption_key_file" || gotPath != path {
			t.Fatalf("trust check = %q %q", field, gotPath)
		}
		return nil
	})
	if err != nil || !checked || len(key) != 32 || key[0] != 0xab {
		t.Fatalf("loadConfigEncryptionKey() = %x, %v checked=%t", key, err, checked)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfigEncryptionKey(path, func(string, string) error { return nil }); err == nil {
		t.Fatal("loadConfigEncryptionKey() accepted a group-readable key")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("AB", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfigEncryptionKey(path, func(string, string) error { return nil }); err == nil {
		t.Fatal("loadConfigEncryptionKey() accepted uppercase hexadecimal")
	}
}

func TestLoadTrustedValidatesBeforeDecode(t *testing.T) {
	wantErr := errors.New("untrusted configuration")
	loadCalled := false
	_, err := loadTrusted(
		"/etc/fire-fuse/config.json",
		func(field, path string) error {
			if field != "configuration" || path != "/etc/fire-fuse/config.json" {
				t.Errorf("trust check = %q %q, want configuration path", field, path)
			}
			return wantErr
		},
		func(string) (Config, error) {
			loadCalled = true
			return Config{}, nil
		},
		func(string) (map[string]ContainerImage, error) {
			t.Fatal("catalog loaded after configuration trust failure")
			return nil, nil
		},
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("loadTrusted() error = %v, want trust failure", err)
	}
	if loadCalled {
		t.Fatal("configuration decoder ran after trust validation failed")
	}
}

func TestLoadTrustedDecodesAfterValidation(t *testing.T) {
	wantConfig := validConfig()
	trustChecks := []string{}
	loadCalled := false
	catalogCalled := false
	got, err := loadTrusted(
		"/etc/fire-fuse/config.json",
		func(field, _ string) error {
			trustChecks = append(trustChecks, field)
			return nil
		},
		func(string) (Config, error) {
			if len(trustChecks) != 1 || trustChecks[0] != "configuration" {
				t.Fatal("configuration decoder ran before trust validation")
			}
			loadCalled = true
			return wantConfig, nil
		},
		func(path string) (map[string]ContainerImage, error) {
			if path != wantConfig.ImageCatalog || len(trustChecks) != 3 || trustChecks[1] != "image_catalog" || trustChecks[2] != "config_encryption_key_file" {
				t.Fatalf("catalog load = %q after trust checks %q", path, trustChecks)
			}
			catalogCalled = true
			return validImages(), nil
		},
	)
	if err != nil {
		t.Fatalf("loadTrusted() error = %v", err)
	}
	if !loadCalled || !catalogCalled || got.FirecrackerBin != wantConfig.FirecrackerBin || len(got.Images) != 1 {
		t.Fatalf("loadTrusted() = %#v, decoder called %t", got, loadCalled)
	}
}

func TestLoadTrustedStopsAfterCatalogTrustFailure(t *testing.T) {
	wantErr := errors.New("untrusted image catalog")
	catalogLoaded := false
	_, err := loadTrusted(
		"/etc/fire-fuse/config.json",
		func(field, _ string) error {
			if field == "image_catalog" {
				return wantErr
			}
			return nil
		},
		func(string) (Config, error) { return validConfig(), nil },
		func(string) (map[string]ContainerImage, error) {
			catalogLoaded = true
			return nil, nil
		},
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("loadTrusted() error = %v, want catalog trust failure", err)
	}
	if catalogLoaded {
		t.Fatal("catalog decoder ran after catalog trust validation failed")
	}
}

func TestLoadTrustedReturnsCatalogDecodeFailure(t *testing.T) {
	wantErr := errors.New("invalid image catalog")
	_, err := loadTrusted(
		"/etc/fire-fuse/config.json",
		func(string, string) error { return nil },
		func(string) (Config, error) { return validConfig(), nil },
		func(string) (map[string]ContainerImage, error) { return nil, wantErr },
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("loadTrusted() error = %v, want catalog decode failure", err)
	}
}

func TestDurationRejectsInvalidJSON(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "number", value: `5000`},
		{name: "invalid string", value: `"soon"`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var duration Duration
			if err := duration.UnmarshalJSON([]byte(test.value)); err == nil {
				t.Fatalf("UnmarshalJSON(%s) error = nil", test.value)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Config)
	}{
		{name: "missing firecracker", change: func(c *Config) { c.FirecrackerBin = "" }},
		{name: "relative jailer", change: func(c *Config) { c.JailerBin = "bin/jailer" }},
		{name: "root jailer UID", change: func(c *Config) { c.JailerUID = 0 }},
		{name: "root jailer GID", change: func(c *Config) { c.JailerGID = 0 }},
		{name: "relative runtime directory", change: func(c *Config) { c.RuntimeDir = "var/fire-fuse" }},
		{name: "root runtime directory", change: func(c *Config) { c.RuntimeDir = "/" }},
		{name: "unclean runtime directory", change: func(c *Config) { c.RuntimeDir = "/var/lib/../fire-fuse" }},
		{name: "missing state database", change: func(c *Config) { c.StateDB = "" }},
		{name: "relative state database", change: func(c *Config) { c.StateDB = "state.db" }},
		{name: "root state database", change: func(c *Config) { c.StateDB = "/" }},
		{name: "missing image catalog", change: func(c *Config) { c.ImageCatalog = "" }},
		{name: "relative image catalog", change: func(c *Config) { c.ImageCatalog = "catalog.json" }},
		{name: "relative configuration encryption key", change: func(c *Config) {
			c.ConfigEncryptionKeyFile = "config-encryption.key"
		}},
		{name: "unclean state database", change: func(c *Config) { c.StateDB = "/var/lib/fire-fuse/../state.db" }},
		{name: "runtime directory as state database", change: func(c *Config) { c.StateDB = c.RuntimeDir }},
		{name: "missing ip binary", change: func(c *Config) { c.Network.IPBin = "" }},
		{name: "relative ip binary", change: func(c *Config) { c.Network.IPBin = "sbin/ip" }},
		{name: "missing iptables binary", change: func(c *Config) { c.Network.IPTablesBin = "" }},
		{name: "relative iptables binary", change: func(c *Config) { c.Network.IPTablesBin = "sbin/iptables" }},
		{name: "missing ip6tables binary", change: func(c *Config) { c.Network.IP6TablesBin = "" }},
		{name: "relative ip6tables binary", change: func(c *Config) { c.Network.IP6TablesBin = "sbin/ip6tables" }},
		{name: "private DNS resolver", change: func(c *Config) { c.Network.Egress.DNSResolvers = []string{"10.0.0.53"} }},
		{name: "IPv6 DNS resolver", change: func(c *Config) { c.Network.Egress.DNSResolvers = []string{"2606:4700:4700::1111"} }},
		{name: "duplicate DNS resolver", change: func(c *Config) { c.Network.Egress.DNSResolvers = []string{"1.1.1.1", "1.1.1.1"} }},
		{name: "zero TCP port", change: func(c *Config) { c.Network.Egress.AllowedTCPPorts = []uint16{0} }},
		{name: "duplicate UDP port", change: func(c *Config) { c.Network.Egress.AllowedUDPPorts = []uint16{443, 443} }},
		{name: "malformed address pool", change: func(c *Config) { c.Network.AddressPool = "not-a-cidr" }},
		{name: "noncanonical address pool", change: func(c *Config) { c.Network.AddressPool = "172.16.0.1/24" }},
		{name: "small address pool", change: func(c *Config) { c.Network.AddressPool = "172.16.0.0/31" }},
		{name: "public address pool", change: func(c *Config) { c.Network.AddressPool = "203.0.113.0/24" }},
		{name: "IPv6 address pool", change: func(c *Config) { c.Network.AddressPool = "fd00::/64" }},
		{name: "relative workspace exportfs binary", change: func(c *Config) {
			c.Workspace.ExportFSBin = "exportfs"
		}},
		{name: "relative workspace mountpoint binary", change: func(c *Config) {
			c.Workspace.MountpointBin = "mountpoint"
		}},
		{name: "root workspace path", change: func(c *Config) {
			c.Workspace.Root = "/"
		}},
		{name: "missing VM limit", change: func(c *Config) { c.Limits.MaxVMs = 0 }},
		{name: "missing memory limit", change: func(c *Config) { c.Limits.MaxTotalMemoryMiB = 0 }},
		{name: "missing concurrent start limit", change: func(c *Config) { c.Limits.MaxConcurrentStarts = 0 }},
		{name: "concurrent starts exceed VMs", change: func(c *Config) { c.Limits.MaxConcurrentStarts = c.Limits.MaxVMs + 1 }},
		{name: "VMs exceed network capacity", change: func(c *Config) {
			c.Network.AddressPool = "172.16.0.0/30"
			c.Limits.MaxVMs = 2
		}},
		{name: "missing startup timeout", change: func(c *Config) { c.Timeouts.Startup = 0 }},
		{name: "negative API socket timeout", change: func(c *Config) { c.Timeouts.APISocket = Duration(-time.Second) }},
		{name: "missing guest shutdown timeout", change: func(c *Config) { c.Timeouts.GuestShutdown = 0 }},
		{name: "missing shutdown grace timeout", change: func(c *Config) { c.Timeouts.ShutdownGrace = 0 }},
		{name: "missing shutdown force timeout", change: func(c *Config) { c.Timeouts.ShutdownForce = 0 }},
		{name: "missing cleanup timeout", change: func(c *Config) { c.Timeouts.Cleanup = 0 }},
		{name: "missing HTTP request read timeout", change: func(c *Config) { c.Timeouts.HTTPRequestRead = 0 }},
		{name: "missing HTTP idle timeout", change: func(c *Config) { c.Timeouts.HTTPIdle = 0 }},
		{name: "missing server shutdown timeout", change: func(c *Config) { c.Timeouts.ServerShutdown = 0 }},
		{name: "no profiles", change: func(c *Config) { c.Profiles = nil }},
		{name: "unsafe profile name", change: func(c *Config) {
			c.Profiles = map[string]Profile{"../ubuntu": validProfile()}
		}},
		{name: "long profile name", change: func(c *Config) {
			c.Profiles = map[string]Profile{strings.Repeat("a", maxProfileNameLength+1): validProfile()}
		}},
		{name: "relative kernel", change: func(c *Config) {
			profile := c.Profiles["default"]
			profile.KernelImage = "images/vmlinux"
			c.Profiles["default"] = profile
		}},
		{name: "missing rootfs", change: func(c *Config) {
			profile := c.Profiles["default"]
			profile.RootFSImage = ""
			c.Profiles["default"] = profile
		}},
		{name: "missing snapshot bundle", change: func(c *Config) {
			profile := c.Profiles["default"]
			profile.SnapshotBundle = ""
			c.Profiles["default"] = profile
		}},
		{name: "blank boot args", change: func(c *Config) {
			profile := c.Profiles["default"]
			profile.BootArgs = "  "
			c.Profiles["default"] = profile
		}},
		{name: "reserved network boot args", change: func(c *Config) {
			profile := c.Profiles["default"]
			profile.BootArgs += " ip=dhcp"
			c.Profiles["default"] = profile
		}},
		{name: "reserved workspace boot args", change: func(c *Config) {
			profile := c.Profiles["default"]
			profile.BootArgs += " fire-fuse.workspace=enabled"
			c.Profiles["default"] = profile
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := validConfig()
			test.change(&cfg)
			if err := cfg.Validate(); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Validate() error = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestValidateAllowsWorkspaceToBeDisabled(t *testing.T) {
	cfg := validConfig()
	cfg.Workspace = nil
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestGetProfileNotFound(t *testing.T) {
	if _, err := validConfig().GetProfile("missing"); !errors.Is(err, ErrProfileNotFound) {
		t.Fatalf("GetProfile() error = %v, want ErrProfileNotFound", err)
	}
}

func TestContainerImageCatalog(t *testing.T) {
	cfg := validConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	imageID := validImageID()
	image, err := cfg.GetImage(imageID)
	if err != nil {
		t.Fatalf("GetImage() error = %v", err)
	}
	if image.RootFSImage != "/var/lib/fire-fuse/images/node-22.ext4" {
		t.Errorf("RootFSImage = %q", image.RootFSImage)
	}
	if _, err := cfg.GetImage("missing"); !errors.Is(err, ErrImageNotFound) {
		t.Fatalf("GetImage() error = %v, want ErrImageNotFound", err)
	}
}

func TestValidateAllowsEmptyContainerImageCatalog(t *testing.T) {
	cfg := validConfig()
	cfg.Images = nil
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestContainerImageIDAddressesRootFSAndProcess(t *testing.T) {
	base := validImage()
	baseID, err := ContainerImageID(base)
	if err != nil {
		t.Fatalf("ContainerImageID() error = %v", err)
	}

	mutations := map[string]func(*ContainerImage){
		"rootfs digest": func(image *ContainerImage) { image.RootFSSHA256 = strings.Repeat("b", 64) },
		"rootfs size":   func(image *ContainerImage) { image.RootFSSizeBytes++ },
		"process":       func(image *ContainerImage) { image.Process.Args = []string{"/bin/other"} },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			image := base
			mutate(&image)
			got, err := ContainerImageID(image)
			if err != nil {
				t.Fatalf("ContainerImageID() error = %v", err)
			}
			if got == baseID {
				t.Fatalf("ContainerImageID() = %q after changing %s", got, name)
			}
		})
	}
}

func TestValidateRejectsContainerImageIdentityMismatch(t *testing.T) {
	cfg := validConfig()
	cfg.Images = map[string]ContainerImage{
		"sha256:" + strings.Repeat("f", 64): validImage(),
	}
	if err := cfg.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Validate() error = %v, want ErrInvalid", err)
	}
}

func TestValidateRejectsInvalidContainerImageCatalog(t *testing.T) {
	tests := []struct {
		name   string
		images map[string]ContainerImage
	}{
		{name: "unsafe name", images: map[string]ContainerImage{"node": validImage()}},
		{name: "relative rootfs", images: map[string]ContainerImage{validImageID(): {
			RootFSImage: "images/node.ext4", RootFSSHA256: strings.Repeat("a", 64), RootFSSizeBytes: 1024, Process: validImage().Process,
		}}},
		{name: "invalid checksum", images: map[string]ContainerImage{validImageID(): {
			RootFSImage: "/images/node.ext4", RootFSSHA256: "invalid", RootFSSizeBytes: 1024, Process: validImage().Process,
		}}},
		{name: "invalid process", images: map[string]ContainerImage{validImageID(): {
			RootFSImage: "/images/node.ext4", RootFSSHA256: strings.Repeat("a", 64), RootFSSizeBytes: 1024,
		}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Images = test.images
			if err := cfg.Validate(); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Validate() error = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestValidateRequiresAbsoluteSnapshotBundle(t *testing.T) {
	cfg := validConfig()
	profile := cfg.Profiles["default"]
	profile.SnapshotBundle = "relative/snapshot"
	cfg.Profiles["default"] = profile
	if err := cfg.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Validate() error = %v, want ErrInvalid", err)
	}
}

func TestValidateHostPoolCapacity(t *testing.T) {
	cfg := validConfig()
	second := cfg.Profiles["default"]
	second.KernelImage = "/images/vmlinux-secondary"
	second.RootFSImage = "/images/rootfs-secondary.ext4"
	second.SnapshotBundle = "/snapshots/secondary"
	cfg.Profiles["secondary"] = second
	cfg.Limits.MaxVMs = 3
	cfg.HostPool.SlotsPerProfile = 2
	if err := cfg.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Validate() error = %v, want ErrInvalid", err)
	}
}

func TestValidateRejectsNegativeHostPoolSize(t *testing.T) {
	cfg := validConfig()
	cfg.HostPool.SlotsPerProfile = -1
	if err := cfg.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Validate() error = %v, want ErrInvalid", err)
	}
}

func validConfig() Config {
	return Config{
		FirecrackerBin: "/usr/local/bin/firecracker",
		JailerBin:      "/usr/local/bin/jailer",
		JailerUID:      1000,
		JailerGID:      1000,
		RuntimeDir:     "/var/lib/fire-fuse",
		StateDB:        "/var/lib/fire-fuse/state.db",
		Network: NetworkConfig{
			IPBin:        "/usr/sbin/ip",
			IPTablesBin:  "/usr/sbin/iptables",
			IP6TablesBin: "/usr/sbin/ip6tables",
			AddressPool:  "172.16.0.0/16",
			Egress: EgressConfig{
				DNSResolvers:    []string{"1.1.1.1", "8.8.8.8"},
				AllowedTCPPorts: []uint16{22, 80, 443},
				AllowedUDPPorts: []uint16{443},
			},
		},
		Workspace: &WorkspaceConfig{
			ExportFSBin:   "/usr/sbin/exportfs",
			JuiceFSBin:    "/usr/local/bin/juicefs",
			MountpointBin: "/usr/bin/mountpoint",
			Root:          "/var/lib/fire-fuse/workspaces",
			AdminRoot:     "/var/lib/fire-fuse/workspace-admin",
		},
		Limits: LimitsConfig{
			MaxVMs:              32,
			MaxTotalMemoryMiB:   32768,
			MaxConcurrentStarts: 2,
		},
		Timeouts: TimeoutsConfig{
			Startup:         Duration(30 * time.Second),
			APISocket:       Duration(5 * time.Second),
			GuestShutdown:   Duration(5 * time.Second),
			ShutdownGrace:   Duration(5 * time.Second),
			ShutdownForce:   Duration(5 * time.Second),
			Cleanup:         Duration(5 * time.Second),
			HTTPRequestRead: Duration(10 * time.Second),
			HTTPIdle:        Duration(30 * time.Second),
			ServerShutdown:  Duration(5 * time.Second),
		},
		ImageCatalog:            "/etc/fire-fuse/image-catalog.json",
		ConfigEncryptionKeyFile: "/etc/fire-fuse/config-encryption.key",
		Profiles: map[string]Profile{
			"default": validProfile(),
		},
		Images: validImages(),
	}
}

func validImage() ContainerImage {
	return ContainerImage{
		RootFSImage:     "/var/lib/fire-fuse/images/node-22.ext4",
		RootFSSHA256:    strings.Repeat("a", 64),
		RootFSSizeBytes: 1024,
		Process: guestconfig.Process{
			Args:             []string{"/bin/server"},
			Env:              []string{"PATH=/usr/bin:/bin"},
			WorkingDirectory: "/app",
			UID:              1000,
			GID:              1000,
		},
	}
}

func validImageID() string {
	id, err := ContainerImageID(validImage())
	if err != nil {
		panic(err)
	}
	return id
}

func validImages() map[string]ContainerImage {
	return map[string]ContainerImage{validImageID(): validImage()}
}

func validProfile() Profile {
	return Profile{
		KernelImage:    "/var/lib/fire-fuse/images/vmlinux",
		RootFSImage:    "/var/lib/fire-fuse/images/ubuntu.ext4",
		SnapshotBundle: "/var/lib/fire-fuse/snapshots/ubuntu-24",
		BootArgs:       "console=ttyS0 reboot=k panic=1",
	}
}
