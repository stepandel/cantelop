package firecracker

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	rootDriveID          = "rootfs"
	containerRootDriveID = "container_rootfs"
	minGuestCID          = 3
)

var (
	ErrInvalidConfiguration  = errors.New("invalid Firecracker configuration")
	resourceIDPattern        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	hostInterfaceNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,14}$`)
)

// MachineConfig describes Firecracker's virtual CPU and memory allocation.
type MachineConfig struct {
	VCPUCount int `json:"vcpu_count"`
	MemoryMiB int `json:"mem_size_mib"`
}

// MemoryHotplugConfig sets the additional memory range before VM boot.
type MemoryHotplugConfig struct {
	TotalSizeMiB int `json:"total_size_mib"`
}

// MemoryHotplugStatus is the guest driver's observed progress toward a target.
type MemoryHotplugStatus struct {
	TotalSizeMiB     int `json:"total_size_mib"`
	PluggedSizeMiB   int `json:"plugged_size_mib"`
	RequestedSizeMiB int `json:"requested_size_mib"`
}

// BootSource describes the guest kernel and its command line.
type BootSource struct {
	KernelImagePath string `json:"kernel_image_path"`
	BootArgs        string `json:"boot_args"`
}

// Drive describes one file-backed block device.
type Drive struct {
	ID           string `json:"drive_id"`
	PathOnHost   string `json:"path_on_host"`
	IsRootDevice bool   `json:"is_root_device"`
	IsReadOnly   bool   `json:"is_read_only"`
}

// NetworkInterface connects one guest network device to a host TAP device.
type NetworkInterface struct {
	InterfaceID    string `json:"iface_id"`
	GuestMAC       string `json:"guest_mac"`
	HostDeviceName string `json:"host_dev_name"`
}

// VSock connects one guest AF_VSOCK device to a host Unix socket.
type VSock struct {
	GuestCID uint32 `json:"guest_cid"`
	UDSPath  string `json:"uds_path"`
}

// SnapshotTemplateConfig boots the image-independent guest state captured by
// the snapshot builder.
type SnapshotTemplateConfig struct {
	VCPUCount           int
	MemoryMiB           int
	HotplugMemoryMiB    int
	KernelImagePath     string
	RootFSPath          string
	ContainerRootFSPath string
	BootArgs            string
	NetworkInterface    *NetworkInterface
	VSock               VSock
}

type instanceAction struct {
	ActionType string `json:"action_type"`
}

type vmState struct {
	State string `json:"state"`
}

type snapshotCreate struct {
	SnapshotType string `json:"snapshot_type"`
	SnapshotPath string `json:"snapshot_path"`
	MemoryPath   string `json:"mem_file_path"`
}

type memoryBackend struct {
	BackendPath string `json:"backend_path"`
	BackendType string `json:"backend_type"`
}

type networkOverride struct {
	InterfaceID    string `json:"iface_id"`
	HostDeviceName string `json:"host_dev_name"`
}

type snapshotLoad struct {
	SnapshotPath     string            `json:"snapshot_path"`
	MemoryBackend    memoryBackend     `json:"mem_backend"`
	TrackDirtyPages  bool              `json:"track_dirty_pages"`
	ResumeVM         bool              `json:"resume_vm"`
	NetworkOverrides []networkOverride `json:"network_overrides"`
}

type partialDrive struct {
	ID         string `json:"drive_id"`
	PathOnHost string `json:"path_on_host"`
}

// ConfigureMachine applies virtual CPU and memory allocation.
func (c *Client) ConfigureMachine(ctx context.Context, config MachineConfig) error {
	if config.VCPUCount < 1 {
		return fmt.Errorf("%w: vCPU count must be at least 1", ErrInvalidConfiguration)
	}
	if config.MemoryMiB < 1 {
		return fmt.Errorf("%w: memory must be at least 1 MiB", ErrInvalidConfiguration)
	}
	return c.Put(ctx, "/machine-config", config)
}

// ConfigureMemoryHotplug adds virtio-mem before boot.
func (c *Client) ConfigureMemoryHotplug(ctx context.Context, sizeMiB int) error {
	if sizeMiB < 128 || sizeMiB%128 != 0 {
		return fmt.Errorf("%w: hotplug size must be a positive multiple of 128 MiB", ErrInvalidConfiguration)
	}
	return c.Put(ctx, "/hotplug/memory", MemoryHotplugConfig{TotalSizeMiB: sizeMiB})
}

// RequestMemoryHotplug changes the target amount of additional guest memory.
func (c *Client) RequestMemoryHotplug(ctx context.Context, sizeMiB int) error {
	if sizeMiB < 0 || sizeMiB%2 != 0 {
		return fmt.Errorf("%w: hotplug target must be a nonnegative multiple of 2 MiB", ErrInvalidConfiguration)
	}
	return c.Patch(ctx, "/hotplug/memory", struct {
		RequestedSizeMiB int `json:"requested_size_mib"`
	}{RequestedSizeMiB: sizeMiB})
}

// ReadMemoryHotplug returns the requested and actually plugged sizes.
func (c *Client) ReadMemoryHotplug(ctx context.Context) (MemoryHotplugStatus, error) {
	var status MemoryHotplugStatus
	err := c.Get(ctx, "/hotplug/memory", &status)
	return status, err
}

// SetBootSource applies the guest kernel image and boot arguments.
func (c *Client) SetBootSource(ctx context.Context, source BootSource) error {
	if !filepath.IsAbs(source.KernelImagePath) {
		return fmt.Errorf("%w: kernel image must be an absolute path", ErrInvalidConfiguration)
	}
	if strings.TrimSpace(source.BootArgs) == "" {
		return fmt.Errorf("%w: boot arguments are required", ErrInvalidConfiguration)
	}
	return c.Put(ctx, "/boot-source", source)
}

// PutDrive attaches or replaces one file-backed block device.
func (c *Client) PutDrive(ctx context.Context, drive Drive) error {
	if !resourceIDPattern.MatchString(drive.ID) {
		return fmt.Errorf("%w: invalid drive ID %q", ErrInvalidConfiguration, drive.ID)
	}
	if !filepath.IsAbs(drive.PathOnHost) {
		return fmt.Errorf("%w: drive path must be absolute", ErrInvalidConfiguration)
	}
	return c.Put(ctx, "/drives/"+drive.ID, drive)
}

// PutNetworkInterface connects a guest network interface to an existing host TAP device.
func (c *Client) PutNetworkInterface(ctx context.Context, networkInterface NetworkInterface) error {
	if err := validateNetworkInterface(networkInterface); err != nil {
		return err
	}
	return c.Put(ctx, "/network-interfaces/"+networkInterface.InterfaceID, networkInterface)
}

// PutVSock configures the guest assignment control channel.
func (c *Client) PutVSock(ctx context.Context, vsock VSock) error {
	if vsock.GuestCID < minGuestCID {
		return fmt.Errorf("%w: vsock guest CID must be at least %d", ErrInvalidConfiguration, minGuestCID)
	}
	if !filepath.IsAbs(vsock.UDSPath) || filepath.Clean(vsock.UDSPath) != vsock.UDSPath {
		return fmt.Errorf("%w: vsock UDS path must be clean and absolute", ErrInvalidConfiguration)
	}
	return c.Put(ctx, "/vsock", vsock)
}

// StartInstance asks Firecracker to boot the configured microVM.
func (c *Client) StartInstance(ctx context.Context) error {
	return c.Put(ctx, "/actions", instanceAction{ActionType: "InstanceStart"})
}

// SendCtrlAltDel asks the guest init system to begin an orderly reboot.
func (c *Client) SendCtrlAltDel(ctx context.Context) error {
	return c.Put(ctx, "/actions", instanceAction{ActionType: "SendCtrlAltDel"})
}

// Pause stops guest vCPU activity while keeping the Firecracker process live.
func (c *Client) Pause(ctx context.Context) error {
	return c.Patch(ctx, "/vm", vmState{State: "Paused"})
}

// Resume restarts a paused guest.
func (c *Client) Resume(ctx context.Context) error {
	return c.Patch(ctx, "/vm", vmState{State: "Resumed"})
}

// CreateFullSnapshot writes guest memory and device state into jailed paths.
func (c *Client) CreateFullSnapshot(
	ctx context.Context,
	snapshotPath,
	memoryPath string,
) error {
	for name, path := range map[string]string{
		"snapshot": snapshotPath,
		"memory":   memoryPath,
	} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path ||
			path == string(filepath.Separator) {
			return fmt.Errorf(
				"%w: %s path must be clean and absolute",
				ErrInvalidConfiguration,
				name,
			)
		}
	}
	if snapshotPath == memoryPath {
		return fmt.Errorf("%w: snapshot and memory paths must differ", ErrInvalidConfiguration)
	}
	return c.putSnapshot(ctx, "/snapshot/create", snapshotCreate{
		SnapshotType: "Full",
		SnapshotPath: snapshotPath,
		MemoryPath:   memoryPath,
	})
}

// LoadSnapshot restores a paused VM while rebinding its saved network
// interface to this instance's TAP.
func (c *Client) LoadSnapshot(
	ctx context.Context,
	snapshotPath,
	memoryPath string,
	networkInterface NetworkInterface,
) error {
	for name, path := range map[string]string{
		"snapshot": snapshotPath,
		"memory":   memoryPath,
	} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path ||
			path == string(filepath.Separator) {
			return fmt.Errorf(
				"%w: %s path must be clean and absolute",
				ErrInvalidConfiguration,
				name,
			)
		}
	}
	if snapshotPath == memoryPath {
		return fmt.Errorf("%w: snapshot and memory paths must differ", ErrInvalidConfiguration)
	}
	if err := validateNetworkInterface(networkInterface); err != nil {
		return err
	}
	return c.putSnapshot(ctx, "/snapshot/load", snapshotLoad{
		SnapshotPath: snapshotPath,
		MemoryBackend: memoryBackend{
			BackendPath: memoryPath,
			BackendType: "File",
		},
		TrackDirtyPages: false,
		ResumeVM:        false,
		NetworkOverrides: []networkOverride{{
			InterfaceID:    networkInterface.InterfaceID,
			HostDeviceName: networkInterface.HostDeviceName,
		}},
	})
}

// PatchDriveBackingFile refreshes a restored block device from its current
// jailed backing path, including a changed file size.
func (c *Client) PatchDriveBackingFile(
	ctx context.Context,
	driveID,
	pathOnHost string,
) error {
	if !resourceIDPattern.MatchString(driveID) {
		return fmt.Errorf("%w: invalid drive ID %q", ErrInvalidConfiguration, driveID)
	}
	if !filepath.IsAbs(pathOnHost) || filepath.Clean(pathOnHost) != pathOnHost ||
		pathOnHost == string(filepath.Separator) {
		return fmt.Errorf("%w: drive path must be clean and absolute", ErrInvalidConfiguration)
	}
	return c.Patch(ctx, "/drives/"+driveID, partialDrive{
		ID:         driveID,
		PathOnHost: pathOnHost,
	})
}

// PatchContainerRootFS refreshes the image-independent snapshot's /dev/vdb
// backing file before the guest is resumed.
func (c *Client) PatchContainerRootFS(ctx context.Context, pathOnHost string) error {
	return c.PatchDriveBackingFile(ctx, containerRootDriveID, pathOnHost)
}

// ConfigureSnapshotTemplate boots a generic guest that waits for one vsock
// assignment without reading its placeholder container drive.
func (c *Client) ConfigureSnapshotTemplate(
	ctx context.Context,
	config SnapshotTemplateConfig,
) error {
	if err := validateSnapshotTemplateConfig(config); err != nil {
		return err
	}
	if err := c.ConfigureMachine(ctx, MachineConfig{
		VCPUCount: config.VCPUCount,
		MemoryMiB: config.MemoryMiB,
	}); err != nil {
		return fmt.Errorf("configure machine: %w", err)
	}
	if err := c.ConfigureMemoryHotplug(ctx, config.HotplugMemoryMiB); err != nil {
		return fmt.Errorf("configure memory hotplug: %w", err)
	}
	if err := c.SetBootSource(ctx, BootSource{
		KernelImagePath: config.KernelImagePath,
		BootArgs:        config.BootArgs,
	}); err != nil {
		return fmt.Errorf("configure boot source: %w", err)
	}
	if err := c.PutDrive(ctx, Drive{
		ID:           rootDriveID,
		PathOnHost:   config.RootFSPath,
		IsRootDevice: true,
		IsReadOnly:   true,
	}); err != nil {
		return fmt.Errorf("configure root drive: %w", err)
	}
	if err := c.PutDrive(ctx, Drive{
		ID:           containerRootDriveID,
		PathOnHost:   config.ContainerRootFSPath,
		IsRootDevice: false,
		IsReadOnly:   true,
	}); err != nil {
		return fmt.Errorf("configure placeholder container drive: %w", err)
	}
	if err := c.PutNetworkInterface(ctx, *config.NetworkInterface); err != nil {
		return fmt.Errorf("configure network interface: %w", err)
	}
	if err := c.PutVSock(ctx, config.VSock); err != nil {
		return fmt.Errorf("configure vsock: %w", err)
	}
	if err := c.StartInstance(ctx); err != nil {
		return fmt.Errorf("start snapshot template: %w", err)
	}
	return nil
}

func validateSnapshotTemplateConfig(config SnapshotTemplateConfig) error {
	if config.VCPUCount < 1 {
		return fmt.Errorf("%w: vCPU count must be at least 1", ErrInvalidConfiguration)
	}
	if config.MemoryMiB < 1 {
		return fmt.Errorf("%w: memory must be at least 1 MiB", ErrInvalidConfiguration)
	}
	for name, path := range map[string]string{
		"kernel image":       config.KernelImagePath,
		"rootfs":             config.RootFSPath,
		"container rootfs":   config.ContainerRootFSPath,
		"vsock UDS endpoint": config.VSock.UDSPath,
	} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return fmt.Errorf("%w: %s must be a clean absolute path", ErrInvalidConfiguration, name)
		}
	}
	if strings.TrimSpace(config.BootArgs) == "" {
		return fmt.Errorf("%w: boot arguments are required", ErrInvalidConfiguration)
	}
	if config.NetworkInterface == nil {
		return fmt.Errorf("%w: network interface is required", ErrInvalidConfiguration)
	}
	if err := validateNetworkInterface(*config.NetworkInterface); err != nil {
		return err
	}
	if config.VSock.GuestCID < minGuestCID {
		return fmt.Errorf("%w: vsock guest CID must be at least %d", ErrInvalidConfiguration, minGuestCID)
	}
	return nil
}

func validateNetworkInterface(networkInterface NetworkInterface) error {
	if !resourceIDPattern.MatchString(networkInterface.InterfaceID) {
		return fmt.Errorf("%w: invalid network interface ID %q", ErrInvalidConfiguration, networkInterface.InterfaceID)
	}
	if !hostInterfaceNamePattern.MatchString(networkInterface.HostDeviceName) {
		return fmt.Errorf("%w: invalid host device name %q", ErrInvalidConfiguration, networkInterface.HostDeviceName)
	}
	guestMAC, err := net.ParseMAC(networkInterface.GuestMAC)
	if err != nil || len(guestMAC) != 6 {
		return fmt.Errorf("%w: invalid guest MAC address %q", ErrInvalidConfiguration, networkInterface.GuestMAC)
	}
	if guestMAC[0]&1 != 0 {
		return fmt.Errorf("%w: guest MAC address must be unicast", ErrInvalidConfiguration)
	}
	return nil
}
