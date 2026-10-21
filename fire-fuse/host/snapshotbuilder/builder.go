// Package snapshotbuilder creates generic, image-independent Firecracker
// snapshot bundles.
package snapshotbuilder

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/guest-runtime/guestassignment"
	"github.com/stepandel/cantelop/fire-fuse/host/firecracker"
	"github.com/stepandel/cantelop/fire-fuse/host/network"
	"github.com/stepandel/cantelop/fire-fuse/host/snapshotbundle"
	"github.com/stepandel/cantelop/fire-fuse/host/snapshotrestore"
)

const (
	templateVMID        = network.SnapshotTemplateVMID
	templateInterfaceID = "eth0"
	templateGuestMAC    = "02:fc:00:00:00:01"
	templateGuestCID    = 3
	snapshotTargetArg   = "systemd.unit=fire-fuse-snapshot.target"
	probeInterval       = 20 * time.Millisecond
	probeAttemptTimeout = 500 * time.Millisecond
	templateProbeDelay  = 2 * time.Second
)

var ErrInvalidConfig = errors.New("invalid snapshot builder configuration")

// Config contains the trusted host inputs for one snapshot build.
type Config struct {
	FirecrackerBin    string
	JailerBin         string
	IPBin             string
	RuntimeDir        string
	KernelImage       string
	PlatformRootFS    string
	Placeholder       string
	Destination       string
	JailerUID         int
	JailerGID         int
	VCPUCount         int
	MemoryMiB         int
	HotplugMemoryMiB  int
	BootArgs          string
	APISocketTimeout  time.Duration
	ShutdownGrace     time.Duration
	ShutdownForce     time.Duration
	GuestReadyTimeout time.Duration
	CleanupTimeout    time.Duration
	Output            io.Writer
}

type managedProcess interface {
	Done() <-chan struct{}
	Stop(context.Context) error
}

type templateClient interface {
	ConfigureSnapshotTemplate(context.Context, firecracker.SnapshotTemplateConfig) error
	Pause(context.Context) error
	CreateFullSnapshot(context.Context, string, string) error
	CloseIdleConnections()
}

type dependencies struct {
	newLayout       func(string, string, string) (firecracker.JailLayout, error)
	prepareAssets   func(context.Context, firecracker.JailLayout, Config) error
	prepareOutput   func(firecracker.JailLayout, int, int) error
	createTAP       func(context.Context, string, int, int) (string, error)
	deleteTAP       func(context.Context, string) error
	launch          func(context.Context, firecracker.JailLayout) (managedProcess, error)
	newClient       func(string) (templateClient, error)
	probeTemplate   func(context.Context, string) error
	probeDelay      time.Duration
	cleanupJail     func(firecracker.JailLayout) error
	publish         func(snapshotbundle.PublishInput) (snapshotbundle.Manifest, error)
	firecrackerInfo func(context.Context, string) (string, error)
	cpuFingerprint  func(string) (string, error)
	now             func() time.Time
}

// Build creates and atomically publishes one generic snapshot bundle.
func Build(ctx context.Context, config Config) (snapshotbundle.Manifest, error) {
	if err := config.validate(); err != nil {
		return snapshotbundle.Manifest{}, err
	}
	deps, err := productionDependencies(config)
	if err != nil {
		return snapshotbundle.Manifest{}, err
	}
	return build(ctx, config, deps)
}

func build(
	ctx context.Context,
	config Config,
	deps dependencies,
) (manifest snapshotbundle.Manifest, err error) {
	if err := config.validate(); err != nil {
		return snapshotbundle.Manifest{}, err
	}
	bootArgs, err := snapshotBootArgs(config.BootArgs)
	if err != nil {
		return snapshotbundle.Manifest{}, err
	}
	version, err := deps.firecrackerInfo(ctx, config.FirecrackerBin)
	if err != nil {
		return snapshotbundle.Manifest{}, err
	}
	cpuFingerprint, err := deps.cpuFingerprint("/proc/cpuinfo")
	if err != nil {
		return snapshotbundle.Manifest{}, fmt.Errorf("fingerprint host CPU: %w", err)
	}
	layout, err := deps.newLayout(config.RuntimeDir, config.FirecrackerBin, templateVMID)
	if err != nil {
		return snapshotbundle.Manifest{}, fmt.Errorf("derive snapshot jail: %w", err)
	}

	jailPrepared := false
	tapCreated := false
	var process managedProcess
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(
			context.WithoutCancel(ctx),
			config.CleanupTimeout,
		)
		defer cancel()
		var cleanupErrors []error
		if process != nil {
			if cleanupErr := process.Stop(cleanupCtx); cleanupErr != nil {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("stop snapshot template: %w", cleanupErr))
			}
		}
		if tapCreated {
			if cleanupErr := deps.deleteTAP(cleanupCtx, templateVMID); cleanupErr != nil {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("delete snapshot TAP: %w", cleanupErr))
			}
		}
		if jailPrepared {
			if cleanupErr := deps.cleanupJail(layout); cleanupErr != nil {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("clean snapshot jail: %w", cleanupErr))
			}
		}
		err = errors.Join(err, errors.Join(cleanupErrors...))
	}()

	if err := deps.prepareAssets(ctx, layout, config); err != nil {
		return snapshotbundle.Manifest{}, fmt.Errorf("prepare snapshot jail: %w", err)
	}
	jailPrepared = true
	if err := deps.prepareOutput(layout, config.JailerUID, config.JailerGID); err != nil {
		return snapshotbundle.Manifest{}, fmt.Errorf("prepare snapshot output: %w", err)
	}
	tapName, err := deps.createTAP(
		ctx,
		templateVMID,
		config.JailerUID,
		config.JailerGID,
	)
	if err != nil {
		return snapshotbundle.Manifest{}, fmt.Errorf("create snapshot TAP: %w", err)
	}
	tapCreated = true
	process, err = deps.launch(ctx, layout)
	if err != nil {
		return snapshotbundle.Manifest{}, fmt.Errorf("launch snapshot template: %w", err)
	}
	client, err := deps.newClient(layout.HostAPISocket)
	if err != nil {
		return snapshotbundle.Manifest{}, fmt.Errorf("create snapshot Firecracker client: %w", err)
	}
	defer client.CloseIdleConnections()
	if err := client.ConfigureSnapshotTemplate(ctx, firecracker.SnapshotTemplateConfig{
		VCPUCount:           config.VCPUCount,
		MemoryMiB:           config.MemoryMiB,
		HotplugMemoryMiB:    config.HotplugMemoryMiB,
		KernelImagePath:     layout.JailedKernel,
		RootFSPath:          layout.JailedRootFS,
		ContainerRootFSPath: layout.JailedContainerRootFS,
		BootArgs:            bootArgs,
		NetworkInterface: &firecracker.NetworkInterface{
			InterfaceID:    templateInterfaceID,
			GuestMAC:       templateGuestMAC,
			HostDeviceName: tapName,
		},
		VSock: firecracker.VSock{
			GuestCID: templateGuestCID,
			UDSPath:  layout.JailedVSock,
		},
	}); err != nil {
		return snapshotbundle.Manifest{}, fmt.Errorf("boot snapshot template: %w", err)
	}
	readyCtx, cancelReady := context.WithTimeout(ctx, config.GuestReadyTimeout)
	err = waitForTemplate(
		readyCtx,
		layout.HostVSock,
		process.Done(),
		deps.probeDelay,
		deps.probeTemplate,
	)
	cancelReady()
	if err != nil {
		return snapshotbundle.Manifest{}, err
	}
	if err := client.Pause(ctx); err != nil {
		return snapshotbundle.Manifest{}, fmt.Errorf("pause snapshot template: %w", err)
	}
	if err := client.CreateFullSnapshot(
		ctx,
		layout.JailedSnapshotState,
		layout.JailedSnapshotMemory,
	); err != nil {
		return snapshotbundle.Manifest{}, fmt.Errorf("create full snapshot: %w", err)
	}
	client.CloseIdleConnections()
	if err := process.Stop(ctx); err != nil {
		return snapshotbundle.Manifest{}, fmt.Errorf("stop snapshotted template: %w", err)
	}
	process = nil

	manifest, err = deps.publish(snapshotbundle.PublishInput{
		Destination:          config.Destination,
		StateSource:          layout.HostSnapshotState,
		MemorySource:         layout.HostSnapshotMemory,
		KernelSource:         config.KernelImage,
		PlatformRootFSSource: config.PlatformRootFS,
		PlaceholderSource:    config.Placeholder,
		FirecrackerVersion:   version,
		CPUFingerprint:       cpuFingerprint,
		CreatedAt:            deps.now(),
		Machine: snapshotbundle.Machine{
			Architecture:     runtime.GOARCH,
			VCPUCount:        config.VCPUCount,
			MemoryMiB:        config.MemoryMiB,
			HotplugMemoryMiB: config.HotplugMemoryMiB,
			GuestCID:         templateGuestCID,
			InterfaceID:      templateInterfaceID,
			GuestMAC:         templateGuestMAC,
		},
		Guest: snapshotbundle.Guest{
			AssignmentProtocol: guestassignment.SchemaVersion,
			PlatformRootPath:   layout.JailedRootFS,
			ContainerRootPath:  layout.JailedContainerRootFS,
			VSockPath:          layout.JailedVSock,
		},
	})
	if err != nil {
		return snapshotbundle.Manifest{}, fmt.Errorf("publish snapshot bundle: %w", err)
	}
	return manifest, nil
}

func productionDependencies(config Config) (dependencies, error) {
	tapManager, err := network.NewTAPManager(config.IPBin)
	if err != nil {
		return dependencies{}, err
	}
	launcher, err := firecracker.NewLauncher(firecracker.LauncherConfig{
		JailerBin:      config.JailerBin,
		FirecrackerBin: config.FirecrackerBin,
		UID:            config.JailerUID,
		GID:            config.JailerGID,
		SocketTimeout:  config.APISocketTimeout,
		ShutdownGrace:  config.ShutdownGrace,
		ShutdownForce:  config.ShutdownForce,
		Output:         config.Output,
	})
	if err != nil {
		return dependencies{}, err
	}
	return dependencies{
		newLayout: firecracker.NewJailLayout,
		prepareAssets: func(
			ctx context.Context,
			layout firecracker.JailLayout,
			config Config,
		) error {
			return layout.PrepareAssetsContext(
				ctx,
				config.KernelImage,
				config.PlatformRootFS,
				config.Placeholder,
				config.JailerUID,
				config.JailerGID,
			)
		},
		prepareOutput: func(layout firecracker.JailLayout, uid, gid int) error {
			return layout.PrepareSnapshotOutput(uid, gid)
		},
		createTAP: tapManager.CreateUnaddressed,
		deleteTAP: tapManager.Delete,
		launch: func(ctx context.Context, layout firecracker.JailLayout) (managedProcess, error) {
			return launcher.Start(ctx, layout)
		},
		newClient:       func(path string) (templateClient, error) { return firecracker.NewClient(path) },
		probeTemplate:   guestassignment.ProbeTemplate,
		probeDelay:      templateProbeDelay,
		cleanupJail:     func(layout firecracker.JailLayout) error { return layout.Cleanup() },
		publish:         snapshotbundle.Publish,
		firecrackerInfo: snapshotrestore.FirecrackerVersion,
		cpuFingerprint:  snapshotrestore.CPUFingerprint,
		now:             time.Now,
	}, nil
}

func waitForTemplate(
	ctx context.Context,
	socketPath string,
	processDone <-chan struct{},
	initialDelay time.Duration,
	probe func(context.Context, string) error,
) error {
	if initialDelay > 0 {
		timer := time.NewTimer(initialDelay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for snapshot assignment barrier: %w", ctx.Err())
		case <-processDone:
			return errors.New("snapshot template exited before assignment barrier")
		case <-timer.C:
		}
	}
	ticker := time.NewTicker(probeInterval)
	defer ticker.Stop()
	var lastErr error
	for {
		attemptCtx, cancel := context.WithTimeout(ctx, probeAttemptTimeout)
		probeErr := probe(attemptCtx, socketPath)
		cancel()
		if probeErr == nil {
			return nil
		}
		lastErr = probeErr
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for snapshot assignment barrier: %w", errors.Join(ctx.Err(), lastErr))
		case <-processDone:
			return errors.New("snapshot template exited before assignment barrier")
		case <-ticker.C:
		}
	}
}

func snapshotBootArgs(base string) (string, error) {
	fields := strings.Fields(base)
	if len(fields) == 0 {
		return "", fmt.Errorf("%w: boot arguments are required", ErrInvalidConfig)
	}
	var result []string
	foundTarget := false
	foundMemoryOnline := false
	for _, field := range fields {
		switch {
		case strings.HasPrefix(field, "ip="):
			return "", fmt.Errorf("%w: snapshot boot arguments cannot configure guest IP", ErrInvalidConfig)
		case strings.HasPrefix(field, "fire-fuse.workspace="):
			return "", fmt.Errorf("%w: snapshot boot arguments cannot configure a workspace", ErrInvalidConfig)
		case strings.HasPrefix(field, "memhp_default_state="):
			if field != "memhp_default_state=online_movable" {
				return "", fmt.Errorf("%w: hotplug memory must be onlined as movable", ErrInvalidConfig)
			}
			foundMemoryOnline = true
			result = append(result, field)
		case strings.HasPrefix(field, "systemd.unit="):
			if !foundTarget {
				result = append(result, snapshotTargetArg)
				foundTarget = true
			}
		default:
			result = append(result, field)
		}
	}
	if !foundTarget {
		result = append(result, snapshotTargetArg)
	}
	if !foundMemoryOnline {
		result = append(result, "memhp_default_state=online_movable")
	}
	return strings.Join(result, " "), nil
}

func (c Config) validate() error {
	for name, path := range map[string]string{
		"firecracker binary": c.FirecrackerBin,
		"jailer binary":      c.JailerBin,
		"ip binary":          c.IPBin,
		"runtime directory":  c.RuntimeDir,
		"kernel image":       c.KernelImage,
		"platform rootfs":    c.PlatformRootFS,
		"placeholder":        c.Placeholder,
		"destination":        c.Destination,
	} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path ||
			path == string(filepath.Separator) {
			return fmt.Errorf("%w: %s must be a clean absolute path", ErrInvalidConfig, name)
		}
	}
	if c.JailerUID < 1 || c.JailerGID < 1 ||
		c.VCPUCount < 1 || c.MemoryMiB < 1 || c.HotplugMemoryMiB < 128 || c.HotplugMemoryMiB%128 != 0 {
		return fmt.Errorf("%w: jail identity and machine dimensions must be positive", ErrInvalidConfig)
	}
	for name, timeout := range map[string]time.Duration{
		"API socket":  c.APISocketTimeout,
		"shutdown":    c.ShutdownGrace,
		"force":       c.ShutdownForce,
		"guest ready": c.GuestReadyTimeout,
		"cleanup":     c.CleanupTimeout,
	} {
		if timeout <= 0 {
			return fmt.Errorf("%w: %s timeout must be positive", ErrInvalidConfig, name)
		}
	}
	if strings.TrimSpace(c.BootArgs) == "" {
		return fmt.Errorf("%w: boot arguments are required", ErrInvalidConfig)
	}
	return nil
}
