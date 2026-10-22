// Package config loads host-owned fire-fuse configuration.
package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/guest-runtime/guestconfig"
	"github.com/stepandel/cantelop/fire-fuse/host/hostpath"
	"github.com/stepandel/cantelop/fire-fuse/runtime/sandboxprofile"
)

const (
	maxProfileNameLength = 64
)

var (
	ErrInvalid         = errors.New("invalid configuration")
	ErrProfileNotFound = errors.New("microVM profile not found")
	ErrImageNotFound   = errors.New("container image not found")
	profileNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	imageIDPattern     = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	sha256Pattern      = regexp.MustCompile(`^[0-9a-f]{64}$`)
	privateIPv4Pools   = []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("172.16.0.0/12"),
		netip.MustParsePrefix("192.168.0.0/16"),
	}
)

// Config contains settings controlled by the fire-fuse host operator.
type Config struct {
	FirecrackerBin          string                    `json:"firecracker_bin"`
	JailerBin               string                    `json:"jailer_bin"`
	JailerUID               int                       `json:"jailer_uid"`
	JailerGID               int                       `json:"jailer_gid"`
	RuntimeDir              string                    `json:"runtime_dir"`
	StateDB                 string                    `json:"state_db"`
	Network                 NetworkConfig             `json:"network"`
	Workspace               *WorkspaceConfig          `json:"workspace,omitempty"`
	HostPool                HostPoolConfig            `json:"host_pool"`
	Limits                  LimitsConfig              `json:"limits"`
	Timeouts                TimeoutsConfig            `json:"timeouts"`
	ImageCatalog            string                    `json:"image_catalog"`
	ConfigEncryptionKeyFile string                    `json:"config_encryption_key_file"`
	Images                  map[string]ContainerImage `json:"-"`
	Profiles                map[string]Profile        `json:"profiles"`
}

// HostPoolConfig controls fixed sandbox capacity per snapshot profile. Each
// unit is ready, claimed by a sandbox, or being prepared. Zero disables the
// pool and retains synchronous preparation.
type HostPoolConfig struct {
	SlotsPerProfile int `json:"slots_per_profile"`
}

// WorkspaceConfig selects the host NFS gateway for persistent workspaces.
type WorkspaceConfig struct {
	ExportFSBin   string `json:"exportfs_bin"`
	JuiceFSBin    string `json:"juicefs_bin"`
	MountpointBin string `json:"mountpoint_bin"`
	Root          string `json:"root"`
	AdminRoot     string `json:"admin_root"`
}

// NetworkConfig contains host-owned tools and address space for microVM links.
type NetworkConfig struct {
	Namespace    string       `json:"namespace,omitempty"`
	IPBin        string       `json:"ip_bin"`
	IPTablesBin  string       `json:"iptables_bin"`
	IP6TablesBin string       `json:"ip6tables_bin"`
	AddressPool  string       `json:"address_pool"`
	Egress       EgressConfig `json:"egress"`
}

// EgressConfig is the host-owned allowlist for guest-initiated public traffic.
// DNS is restricted to exact resolvers; other protocols are restricted by
// destination port and can never target host-private or special-use networks.
type EgressConfig struct {
	DNSResolvers    []string `json:"dns_resolvers"`
	AllowedTCPPorts []uint16 `json:"allowed_tcp_ports"`
	AllowedUDPPorts []uint16 `json:"allowed_udp_ports"`
}

// LimitsConfig defines host-wide lifecycle admission ceilings.
type LimitsConfig struct {
	MaxVMs              int `json:"max_vms"`
	MaxTotalMemoryMiB   int `json:"max_total_memory_mib"`
	MaxConcurrentStarts int `json:"max_concurrent_starts"`
}

// Duration is a human-readable duration decoded using time.ParseDuration.
type Duration time.Duration

// UnmarshalJSON requires durations to be JSON strings such as "5s" or "2m".
func (d *Duration) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("duration must be a string: %w", err)
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", value, err)
	}
	*d = Duration(parsed)
	return nil
}

// TimeoutsConfig defines deadlines for bounded host-side operations.
type TimeoutsConfig struct {
	Startup         Duration `json:"startup"`
	APISocket       Duration `json:"api_socket"`
	GuestShutdown   Duration `json:"guest_shutdown"`
	ShutdownGrace   Duration `json:"shutdown_grace"`
	ShutdownForce   Duration `json:"shutdown_force"`
	Cleanup         Duration `json:"cleanup"`
	HTTPRequestRead Duration `json:"http_request_read"`
	HTTPIdle        Duration `json:"http_idle"`
	ServerShutdown  Duration `json:"server_shutdown"`
}

// Profile selects the immutable platform snapshot for one VM shape.
type Profile struct {
	KernelImage    string `json:"kernel_image"`
	RootFSImage    string `json:"rootfs_image"`
	SnapshotBundle string `json:"snapshot_bundle"`
	BootArgs       string `json:"boot_args"`
}

// ContainerImage is one immutable, host-owned workload artifact selectable by
// its API-safe catalog identifier.
type ContainerImage struct {
	RootFSImage     string              `json:"rootfs_image"`
	RootFSSHA256    string              `json:"rootfs_sha256"`
	RootFSSizeBytes int64               `json:"rootfs_size_bytes"`
	Process         guestconfig.Process `json:"process"`
}

type trustedFileChecker func(string, string) error
type configLoader func(string) (Config, error)
type imageCatalogLoader func(string) (map[string]ContainerImage, error)

// LoadTrusted validates the host ownership boundary before decoding a
// configuration that can select privileged binaries and filesystem paths.
func LoadTrusted(path string) (Config, error) {
	return loadTrusted(path, hostpath.CheckTrustedFile, Load, LoadImageCatalog)
}

func loadTrusted(
	path string,
	checkTrustedFile trustedFileChecker,
	load configLoader,
	loadCatalog imageCatalogLoader,
) (Config, error) {
	if err := checkTrustedFile("configuration", path); err != nil {
		return Config{}, fmt.Errorf("validate configuration file trust: %w", err)
	}
	cfg, err := load(path)
	if err != nil {
		return Config{}, err
	}
	if err := checkTrustedFile("image_catalog", cfg.ImageCatalog); err != nil {
		return Config{}, fmt.Errorf("validate image catalog trust: %w", err)
	}
	if err := checkTrustedFile("config_encryption_key_file", cfg.ConfigEncryptionKeyFile); err != nil {
		return Config{}, fmt.Errorf("validate configuration encryption key trust: %w", err)
	}
	images, err := loadCatalog(cfg.ImageCatalog)
	if err != nil {
		return Config{}, fmt.Errorf("load image catalog: %w", err)
	}
	cfg.Images = images
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Load decodes one strict JSON document and validates its structure.
func Load(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open configuration: %w", err)
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()

	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode configuration: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("%w: file must contain exactly one JSON object", ErrInvalid)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// Validate checks configuration structure without inspecting the host filesystem.
func (c Config) Validate() error {
	if err := validateAbsolutePath("firecracker_bin", c.FirecrackerBin); err != nil {
		return err
	}
	if err := validateAbsolutePath("jailer_bin", c.JailerBin); err != nil {
		return err
	}
	if c.JailerUID < 1 {
		return fmt.Errorf("%w: jailer_uid must be a non-root UID", ErrInvalid)
	}
	if c.JailerGID < 1 {
		return fmt.Errorf("%w: jailer_gid must be a non-root GID", ErrInvalid)
	}
	if err := validateAbsolutePath("runtime_dir", c.RuntimeDir); err != nil {
		return err
	}
	if err := validateAbsolutePath("state_db", c.StateDB); err != nil {
		return err
	}
	if c.StateDB == c.RuntimeDir {
		return fmt.Errorf("%w: state_db must name a file, not runtime_dir", ErrInvalid)
	}
	if err := validateAbsolutePath("network.ip_bin", c.Network.IPBin); err != nil {
		return err
	}
	if err := validateAbsolutePath("network.iptables_bin", c.Network.IPTablesBin); err != nil {
		return err
	}
	if err := validateAbsolutePath("network.ip6tables_bin", c.Network.IP6TablesBin); err != nil {
		return err
	}
	if err := validateAddressPool(c.Network.AddressPool); err != nil {
		return err
	}
	if c.Network.Namespace != "" && c.Network.Namespace != "managed" {
		return errors.New("network namespace must be empty or managed")
	}
	if err := validateEgress(c.Network.Egress); err != nil {
		return err
	}
	if c.Workspace != nil {
		if err := validateAbsolutePath("workspace.exportfs_bin", c.Workspace.ExportFSBin); err != nil {
			return err
		}
		if err := validateAbsolutePath("workspace.juicefs_bin", c.Workspace.JuiceFSBin); err != nil {
			return err
		}
		if err := validateAbsolutePath("workspace.mountpoint_bin", c.Workspace.MountpointBin); err != nil {
			return err
		}
		if err := validateAbsolutePath("workspace.root", c.Workspace.Root); err != nil {
			return err
		}
		if err := validateAbsolutePath("workspace.admin_root", c.Workspace.AdminRoot); err != nil {
			return err
		}
		if c.Workspace.AdminRoot == c.Workspace.Root {
			return fmt.Errorf("%w: workspace.admin_root must differ from workspace.root", ErrInvalid)
		}
	}
	if err := validateLimits(c.Limits, c.Network.AddressPool); err != nil {
		return err
	}
	if err := validateTimeouts(c.Timeouts); err != nil {
		return err
	}
	if err := validateAbsolutePath("image_catalog", c.ImageCatalog); err != nil {
		return err
	}
	if err := validateAbsolutePath("config_encryption_key_file", c.ConfigEncryptionKeyFile); err != nil {
		return err
	}
	if len(c.Profiles) != 1 {
		return fmt.Errorf("%w: profiles must contain exactly one entry named %q", ErrInvalid, sandboxprofile.DefaultName)
	}
	if _, exists := c.Profiles[sandboxprofile.DefaultName]; !exists {
		return fmt.Errorf("%w: profiles must contain exactly one entry named %q", ErrInvalid, sandboxprofile.DefaultName)
	}

	for name, profile := range c.Profiles {
		if len(name) > maxProfileNameLength || !profileNamePattern.MatchString(name) {
			return fmt.Errorf("%w: invalid profile name %q", ErrInvalid, name)
		}
		if err := validateAbsolutePath("profiles."+name+".kernel_image", profile.KernelImage); err != nil {
			return err
		}
		if err := validateAbsolutePath("profiles."+name+".rootfs_image", profile.RootFSImage); err != nil {
			return err
		}
		if err := validateAbsolutePath(
			"profiles."+name+".snapshot_bundle",
			profile.SnapshotBundle,
		); err != nil {
			return err
		}
		if strings.TrimSpace(profile.BootArgs) == "" {
			return fmt.Errorf("%w: profiles.%s.boot_args is required", ErrInvalid, name)
		}
		for _, argument := range strings.Fields(profile.BootArgs) {
			if strings.HasPrefix(argument, "ip=") {
				return fmt.Errorf("%w: profiles.%s.boot_args cannot set reserved ip= networking", ErrInvalid, name)
			}
			if strings.HasPrefix(argument, "fire-fuse.workspace=") {
				return fmt.Errorf(
					"%w: profiles.%s.boot_args cannot set reserved workspace activation",
					ErrInvalid,
					name,
				)
			}
		}
	}
	if err := validateHostPool(c.HostPool, c.Limits, len(c.Profiles)); err != nil {
		return err
	}
	if err := validateContainerImages(c.Images); err != nil {
		return err
	}

	return nil
}

// LoadConfigEncryptionKey reads one root-owned mode-0600 lowercase-hex key.
// The returned bytes should be cleared by the caller after opening the store.
func LoadConfigEncryptionKey(path string) ([]byte, error) {
	return loadConfigEncryptionKey(path, hostpath.CheckTrustedFile)
}

func loadConfigEncryptionKey(path string, check trustedFileChecker) ([]byte, error) {
	if err := check("config_encryption_key_file", path); err != nil {
		return nil, fmt.Errorf("validate configuration encryption key trust: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect configuration encryption key: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || (info.Size() != 64 && info.Size() != 65) {
		return nil, errors.New("configuration encryption key must be a private regular file with one 64-character value")
	}
	document, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read configuration encryption key: %w", err)
	}
	defer clear(document)
	document = bytes.TrimSuffix(document, []byte("\n"))
	if len(document) != 64 || !sha256Pattern.Match(document) {
		return nil, errors.New("configuration encryption key must contain exactly 64 lowercase hexadecimal characters")
	}
	key := make([]byte, 32)
	if _, err := hex.Decode(key, document); err != nil {
		return nil, errors.New("configuration encryption key is not valid hexadecimal")
	}
	return key, nil
}

func validateHostPool(pool HostPoolConfig, limits LimitsConfig, profiles int) error {
	if pool.SlotsPerProfile < 0 || pool.SlotsPerProfile > limits.MaxVMs {
		return fmt.Errorf("%w: host_pool.slots_per_profile must be between 0 and limits.max_vms", ErrInvalid)
	}
	totalCapacity := uint64(pool.SlotsPerProfile) * uint64(profiles)
	if totalCapacity > uint64(limits.MaxVMs) {
		return fmt.Errorf(
			"%w: host pool capacity %d exceeds limits.max_vms %d",
			ErrInvalid, totalCapacity, limits.MaxVMs,
		)
	}
	return nil
}

func validateEgress(egress EgressConfig) error {
	if len(egress.DNSResolvers) > 4 {
		return fmt.Errorf("%w: network.egress.dns_resolvers supports at most 4 addresses", ErrInvalid)
	}
	seenResolvers := make(map[netip.Addr]struct{}, len(egress.DNSResolvers))
	for _, value := range egress.DNSResolvers {
		resolver, err := netip.ParseAddr(value)
		if err != nil || !resolver.Is4() || !resolver.IsGlobalUnicast() ||
			resolver.IsPrivate() || resolver.IsLoopback() || resolver.IsLinkLocalUnicast() {
			return fmt.Errorf("%w: network.egress.dns_resolvers must contain public IPv4 addresses", ErrInvalid)
		}
		if _, exists := seenResolvers[resolver]; exists {
			return fmt.Errorf("%w: network.egress.dns_resolvers contains duplicate %s", ErrInvalid, resolver)
		}
		seenResolvers[resolver] = struct{}{}
	}
	for name, ports := range map[string][]uint16{
		"allowed_tcp_ports": egress.AllowedTCPPorts,
		"allowed_udp_ports": egress.AllowedUDPPorts,
	} {
		if len(ports) > 15 {
			return fmt.Errorf("%w: network.egress.%s supports at most 15 ports", ErrInvalid, name)
		}
		seen := make(map[uint16]struct{}, len(ports))
		for _, port := range ports {
			if port == 0 {
				return fmt.Errorf("%w: network.egress.%s cannot contain port 0", ErrInvalid, name)
			}
			if _, exists := seen[port]; exists {
				return fmt.Errorf("%w: network.egress.%s contains duplicate port %d", ErrInvalid, name, port)
			}
			seen[port] = struct{}{}
		}
	}
	return nil
}

func validateTimeouts(timeouts TimeoutsConfig) error {
	fields := []struct {
		name  string
		value Duration
	}{
		{name: "timeouts.startup", value: timeouts.Startup},
		{name: "timeouts.api_socket", value: timeouts.APISocket},
		{name: "timeouts.guest_shutdown", value: timeouts.GuestShutdown},
		{name: "timeouts.shutdown_grace", value: timeouts.ShutdownGrace},
		{name: "timeouts.shutdown_force", value: timeouts.ShutdownForce},
		{name: "timeouts.cleanup", value: timeouts.Cleanup},
		{name: "timeouts.http_request_read", value: timeouts.HTTPRequestRead},
		{name: "timeouts.http_idle", value: timeouts.HTTPIdle},
		{name: "timeouts.server_shutdown", value: timeouts.ServerShutdown},
	}
	for _, field := range fields {
		if field.value <= 0 {
			return fmt.Errorf("%w: %s must be positive", ErrInvalid, field.name)
		}
	}
	return nil
}

func validateLimits(limits LimitsConfig, addressPool string) error {
	fields := []struct {
		name  string
		value int
	}{
		{name: "limits.max_vms", value: limits.MaxVMs},
		{name: "limits.max_total_memory_mib", value: limits.MaxTotalMemoryMiB},
		{name: "limits.max_concurrent_starts", value: limits.MaxConcurrentStarts},
	}
	for _, field := range fields {
		if field.value < 1 {
			return fmt.Errorf("%w: %s must be positive", ErrInvalid, field.name)
		}
	}
	if limits.MaxConcurrentStarts > limits.MaxVMs {
		return fmt.Errorf(
			"%w: limits.max_concurrent_starts cannot exceed limits.max_vms",
			ErrInvalid,
		)
	}
	pool, err := netip.ParsePrefix(addressPool)
	if err != nil {
		return fmt.Errorf("%w: cannot calculate network capacity for limits", ErrInvalid)
	}
	addressCapacity := uint64(1) << (30 - pool.Bits())
	if uint64(limits.MaxVMs) > addressCapacity {
		return fmt.Errorf(
			"%w: limits.max_vms exceeds network.address_pool capacity of %d",
			ErrInvalid,
			addressCapacity,
		)
	}
	return nil
}

// GetProfile resolves an API-facing profile name to host-owned boot artifacts.
func (c Config) GetProfile(name string) (Profile, error) {
	profile, exists := c.Profiles[name]
	if !exists {
		return Profile{}, fmt.Errorf("%w: %s", ErrProfileNotFound, name)
	}
	return profile, nil
}

// GetImage resolves an API-facing image ID to trusted workload artifacts.
func (c Config) GetImage(name string) (ContainerImage, error) {
	image, exists := c.Images[name]
	if !exists {
		return ContainerImage{}, fmt.Errorf("%w: %s", ErrImageNotFound, name)
	}
	return image, nil
}

// ContainerImageID returns the content address for a materialized rootfs and
// its canonical process configuration.
func ContainerImageID(image ContainerImage) (string, error) {
	if !sha256Pattern.MatchString(image.RootFSSHA256) {
		return "", errors.New("rootfs_sha256 must be lowercase SHA-256")
	}
	if image.RootFSSizeBytes < 1 {
		return "", errors.New("rootfs_size_bytes must be positive")
	}
	if err := (guestconfig.Config{
		SchemaVersion: guestconfig.SchemaVersion,
		Process:       image.Process,
	}).Validate(); err != nil {
		return "", fmt.Errorf("invalid process: %w", err)
	}
	processJSON, err := json.Marshal(image.Process)
	if err != nil {
		return "", fmt.Errorf("encode canonical process: %w", err)
	}
	processDigest := sha256.Sum256(processJSON)
	identityJSON, err := json.Marshal(struct {
		SchemaVersion   int    `json:"schema_version"`
		RootFSSHA256    string `json:"rootfs_sha256"`
		RootFSSizeBytes int64  `json:"rootfs_size_bytes"`
		ProcessSHA256   string `json:"process_sha256"`
	}{
		SchemaVersion:   1,
		RootFSSHA256:    image.RootFSSHA256,
		RootFSSizeBytes: image.RootFSSizeBytes,
		ProcessSHA256:   fmt.Sprintf("%x", processDigest),
	})
	if err != nil {
		return "", fmt.Errorf("encode image identity: %w", err)
	}
	digest := sha256.Sum256(identityJSON)
	return fmt.Sprintf("sha256:%x", digest), nil
}

func validateAbsolutePath(field, path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("%w: %s is required", ErrInvalid, field)
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("%w: %s must be an absolute path", ErrInvalid, field)
	}
	if filepath.Clean(path) != path {
		return fmt.Errorf("%w: %s must already be a clean path", ErrInvalid, field)
	}
	if path == string(filepath.Separator) {
		return fmt.Errorf("%w: %s cannot be the filesystem root", ErrInvalid, field)
	}
	return nil
}

func validateAddressPool(value string) error {
	pool, err := netip.ParsePrefix(value)
	if err != nil || !pool.Addr().Is4() || pool != pool.Masked() {
		return fmt.Errorf("%w: network.address_pool must be a canonical IPv4 CIDR", ErrInvalid)
	}
	if pool.Bits() > 30 {
		return fmt.Errorf("%w: network.address_pool must contain at least one /30 subnet", ErrInvalid)
	}
	for _, privatePool := range privateIPv4Pools {
		if pool.Bits() >= privatePool.Bits() && privatePool.Contains(pool.Addr()) {
			return nil
		}
	}
	return fmt.Errorf("%w: network.address_pool must be entirely private", ErrInvalid)
}
