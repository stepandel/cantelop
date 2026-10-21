package snapshotbuilder

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/guest-runtime/guestassignment"
	"github.com/stepandel/cantelop/fire-fuse/host/firecracker"
	"github.com/stepandel/cantelop/fire-fuse/host/snapshotbundle"
)

type fakeProcess struct {
	done      chan struct{}
	stopCalls int
}

func (p *fakeProcess) Done() <-chan struct{} { return p.done }
func (p *fakeProcess) Stop(context.Context) error {
	p.stopCalls++
	return nil
}

type fakeTemplateClient struct {
	layout    firecracker.JailLayout
	config    firecracker.SnapshotTemplateConfig
	calls     []string
	configErr error
}

func (c *fakeTemplateClient) ConfigureSnapshotTemplate(
	_ context.Context,
	config firecracker.SnapshotTemplateConfig,
) error {
	c.calls = append(c.calls, "configure")
	c.config = config
	return c.configErr
}

func (c *fakeTemplateClient) Pause(context.Context) error {
	c.calls = append(c.calls, "pause")
	return nil
}

func (c *fakeTemplateClient) CreateFullSnapshot(
	_ context.Context,
	statePath,
	memoryPath string,
) error {
	c.calls = append(c.calls, "snapshot")
	if statePath != c.layout.JailedSnapshotState ||
		memoryPath != c.layout.JailedSnapshotMemory {
		return errors.New("unexpected jailed snapshot paths")
	}
	if err := os.WriteFile(c.layout.HostSnapshotState, []byte("state"), 0o600); err != nil {
		return err
	}
	return os.WriteFile(c.layout.HostSnapshotMemory, []byte("memory"), 0o600)
}

func (c *fakeTemplateClient) CloseIdleConnections() {
	c.calls = append(c.calls, "close")
}

func TestBuildCreatesGenericSnapshotAndCleansTemplateResources(t *testing.T) {
	config, layout := testBuildConfig(t)
	process := &fakeProcess{done: make(chan struct{})}
	client := &fakeTemplateClient{layout: layout}
	var lifecycle []string
	deps := testDependencies(t, config, layout, process, client, &lifecycle)

	manifest, err := build(context.Background(), config, deps)
	if err != nil {
		t.Fatalf("build() error = %v", err)
	}
	if manifest.Machine.VCPUCount != 2 ||
		manifest.Machine.MemoryMiB != 512 ||
		manifest.Guest.AssignmentProtocol != guestassignment.SchemaVersion {
		t.Fatalf("manifest = %#v", manifest)
	}
	if client.config.BootArgs !=
		"console=ttyS0 quiet systemd.unit=fire-fuse-snapshot.target memhp_default_state=online_movable" {
		t.Fatalf("snapshot boot arguments = %q", client.config.BootArgs)
	}
	if client.config.ContainerRootFSPath != layout.JailedContainerRootFS ||
		client.config.VSock.UDSPath != layout.JailedVSock {
		t.Fatalf("template config = %#v", client.config)
	}
	if got, want := client.calls, []string{
		"configure", "pause", "snapshot", "close", "close",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("client calls = %q, want %q", got, want)
	}
	if process.stopCalls != 1 {
		t.Fatalf("process stop calls = %d", process.stopCalls)
	}
	if got, want := lifecycle, []string{
		"prepare-assets",
		"prepare-output",
		"create-tap",
		"launch",
		"probe",
		"publish",
		"delete-tap",
		"cleanup-jail",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("lifecycle = %q, want %q", got, want)
	}
	if _, err := snapshotbundle.Load(
		filepath.Join(config.Destination, snapshotbundle.ManifestName),
	); err != nil {
		t.Fatalf("load published bundle: %v", err)
	}
}

func TestBuildCleansResourcesAfterBootFailure(t *testing.T) {
	config, layout := testBuildConfig(t)
	process := &fakeProcess{done: make(chan struct{})}
	client := &fakeTemplateClient{
		layout:    layout,
		configErr: errors.New("boot failed"),
	}
	var lifecycle []string
	deps := testDependencies(t, config, layout, process, client, &lifecycle)

	if _, err := build(context.Background(), config, deps); err == nil {
		t.Fatal("build() error = nil")
	}
	if process.stopCalls != 1 {
		t.Fatalf("process stop calls = %d", process.stopCalls)
	}
	for _, want := range []string{"delete-tap", "cleanup-jail"} {
		if !contains(lifecycle, want) {
			t.Errorf("lifecycle is missing %q: %q", want, lifecycle)
		}
	}
	if _, err := os.Stat(config.Destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed build published destination: %v", err)
	}
}

func TestWaitForTemplateDoesNotProbeBeforeInitialDelay(t *testing.T) {
	processDone := make(chan struct{})
	close(processDone)
	probes := 0
	err := waitForTemplate(
		context.Background(),
		"/jail/run/fire-fuse.vsock",
		processDone,
		time.Minute,
		func(context.Context, string) error {
			probes++
			return nil
		},
	)
	if err == nil || !strings.Contains(err.Error(), "exited before assignment barrier") {
		t.Fatalf("waitForTemplate() error = %v", err)
	}
	if probes != 0 {
		t.Fatalf("probe calls = %d, want 0", probes)
	}
}

func TestSnapshotBootArgsSelectsSnapshotTarget(t *testing.T) {
	got, err := snapshotBootArgs(
		"console=ttyS0 systemd.unit=multi-user.target quiet",
	)
	if err != nil {
		t.Fatal(err)
	}
	if got != "console=ttyS0 systemd.unit=fire-fuse-snapshot.target quiet memhp_default_state=online_movable" {
		t.Fatalf("snapshotBootArgs() = %q", got)
	}
	for _, argument := range []string{
		"console=ttyS0 ip=172.16.0.2::172.16.0.1",
		"console=ttyS0 fire-fuse.workspace=enabled",
	} {
		if _, err := snapshotBootArgs(argument); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("snapshotBootArgs(%q) error = %v", argument, err)
		}
	}
}

func testBuildConfig(t *testing.T) (Config, firecracker.JailLayout) {
	t.Helper()
	root := t.TempDir()
	write := func(name, contents string) string {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	layout := firecracker.JailLayout{
		HostAPISocket:         "/jail/run/firecracker.socket",
		JailedKernel:          "/vmlinux",
		JailedRootFS:          "/rootfs.ext4",
		JailedContainerRootFS: "/container-rootfs.ext4",
		HostVSock:             "/jail/run/fire-fuse.vsock",
		JailedVSock:           "/run/fire-fuse.vsock",
		HostSnapshotDirectory: filepath.Join(root, "snapshot-output"),
		HostSnapshotState:     filepath.Join(root, "snapshot-output/vmstate"),
		JailedSnapshotState:   "/snapshot/vmstate",
		HostSnapshotMemory:    filepath.Join(root, "snapshot-output/memory"),
		JailedSnapshotMemory:  "/snapshot/memory",
	}
	return Config{
		FirecrackerBin:    "/usr/bin/firecracker",
		JailerBin:         "/usr/bin/jailer",
		IPBin:             "/usr/sbin/ip",
		RuntimeDir:        "/var/lib/fire-fuse",
		KernelImage:       write("vmlinux", "kernel"),
		PlatformRootFS:    write("rootfs", "rootfs"),
		Placeholder:       write("placeholder", "placeholder"),
		Destination:       filepath.Join(root, "bundle"),
		JailerUID:         1000,
		JailerGID:         1001,
		VCPUCount:         2,
		MemoryMiB:         512,
		HotplugMemoryMiB:  4096,
		BootArgs:          "console=ttyS0 quiet systemd.unit=fire-fuse-snapshot.target",
		APISocketTimeout:  time.Second,
		ShutdownGrace:     time.Second,
		ShutdownForce:     time.Second,
		GuestReadyTimeout: time.Second,
		CleanupTimeout:    time.Second,
	}, layout
}

func testDependencies(
	t *testing.T,
	config Config,
	layout firecracker.JailLayout,
	process *fakeProcess,
	client *fakeTemplateClient,
	lifecycle *[]string,
) dependencies {
	t.Helper()
	return dependencies{
		newLayout: func(string, string, string) (firecracker.JailLayout, error) {
			return layout, nil
		},
		prepareAssets: func(context.Context, firecracker.JailLayout, Config) error {
			*lifecycle = append(*lifecycle, "prepare-assets")
			return nil
		},
		prepareOutput: func(firecracker.JailLayout, int, int) error {
			*lifecycle = append(*lifecycle, "prepare-output")
			return os.MkdirAll(layout.HostSnapshotDirectory, 0o700)
		},
		createTAP: func(context.Context, string, int, int) (string, error) {
			*lifecycle = append(*lifecycle, "create-tap")
			return "ff0123456789abc", nil
		},
		deleteTAP: func(context.Context, string) error {
			*lifecycle = append(*lifecycle, "delete-tap")
			return nil
		},
		launch: func(context.Context, firecracker.JailLayout) (managedProcess, error) {
			*lifecycle = append(*lifecycle, "launch")
			return process, nil
		},
		newClient: func(string) (templateClient, error) { return client, nil },
		probeTemplate: func(context.Context, string) error {
			*lifecycle = append(*lifecycle, "probe")
			return nil
		},
		cleanupJail: func(firecracker.JailLayout) error {
			*lifecycle = append(*lifecycle, "cleanup-jail")
			return nil
		},
		publish: func(input snapshotbundle.PublishInput) (snapshotbundle.Manifest, error) {
			*lifecycle = append(*lifecycle, "publish")
			return snapshotbundle.Publish(input)
		},
		firecrackerInfo: func(context.Context, string) (string, error) {
			return "Firecracker v1.15.1", nil
		},
		cpuFingerprint: func(string) (string, error) {
			sum := sha256.Sum256([]byte("cpu"))
			return hex.EncodeToString(sum[:]), nil
		},
		now: func() time.Time {
			return time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
		},
	}
}

func contains(values []string, want string) bool {
	return strings.Contains("\x00"+strings.Join(values, "\x00")+"\x00", "\x00"+want+"\x00")
}
