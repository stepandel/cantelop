package guestrunner

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stepandel/cantelop/fire-fuse/guest-runtime/guestconfig"
)

func TestEnsureDirectoryRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "escape")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	_, err := ensureDirectory(root, "/escape/workspace", 0o755)
	if err == nil || !strings.Contains(err.Error(), "crosses a non-directory") {
		t.Fatalf("ensureDirectory() error = %v, want symlink rejection", err)
	}
}

func TestEnsureDirectoryCreatesContainedPath(t *testing.T) {
	root := t.TempDir()
	got, err := ensureDirectory(root, "/var/lib/application", 0o750)
	if err != nil {
		t.Fatalf("ensureDirectory(): %v", err)
	}
	want := filepath.Join(root, "var", "lib", "application")
	if got != want {
		t.Fatalf("ensureDirectory() = %q, want %q", got, want)
	}
	info, err := os.Stat(got)
	if err != nil {
		t.Fatalf("stat directory: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("%s is not a directory", got)
	}
}

func TestEnsureRegularFileRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "etc"), 0o755); err != nil {
		t.Fatalf("create etc: %v", err)
	}
	if err := os.Symlink("/tmp/escape", filepath.Join(root, "etc", "resolv.conf")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	_, err := ensureRegularFile(root, "/etc/resolv.conf")
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("ensureRegularFile() error = %v, want symlink rejection", err)
	}
}

func TestMinimalDeviceContract(t *testing.T) {
	wantDevices := []string{"null", "zero", "full", "random", "urandom", "tty"}
	if !reflect.DeepEqual(minimalContainerDevices, wantDevices) {
		t.Fatalf(
			"minimalContainerDevices = %q, want %q",
			minimalContainerDevices,
			wantDevices,
		)
	}
	wantLinks := []deviceLink{
		{name: "fd", destination: "/proc/self/fd"},
		{name: "stdin", destination: "/proc/self/fd/0"},
		{name: "stdout", destination: "/proc/self/fd/1"},
		{name: "stderr", destination: "/proc/self/fd/2"},
		{name: "ptmx", destination: "pts/ptmx"},
	}
	if !reflect.DeepEqual(minimalContainerDeviceLinks, wantLinks) {
		t.Fatalf(
			"minimalContainerDeviceLinks = %#v, want %#v",
			minimalContainerDeviceLinks,
			wantLinks,
		)
	}
}

func TestLinuxMountPolicyIsMinimalAndReadOnly(t *testing.T) {
	source, err := os.ReadFile("platform_linux.go")
	if err != nil {
		t.Fatalf("read Linux platform: %v", err)
	}
	contents := string(source)
	for _, want := range []string{
		`"mode=0755,size=1m"`,
		`"newinstance,ptmxmode=0666,mode=0620,gid=5"`,
		`"mode=1777,size=64m"`,
		"unix.MS_RDONLY|unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC",
		`"hidepid=2"`,
		`"sysfs"`,
	} {
		if !strings.Contains(contents, want) {
			t.Errorf("Linux mount policy does not contain %q", want)
		}
	}
}

func TestLinuxPrivilegePolicyIsFailClosed(t *testing.T) {
	source, err := os.ReadFile("privileges_linux_amd64.go")
	if err != nil {
		t.Fatalf("read Linux privilege policy: %v", err)
	}
	contents := string(source)
	for _, want := range []string{
		"PR_CAPBSET_DROP",
		"Capset",
		"PR_SET_NO_NEW_PRIVS",
		"PR_SET_SECCOMP",
		"SECCOMP_RET_ERRNO",
		"SECCOMP_RET_KILL_PROCESS",
		`"/proc/sys/kernel/cap_last_cap"`,
	} {
		if !strings.Contains(contents, want) {
			t.Errorf("Linux privilege policy does not contain %q", want)
		}
	}
}

func TestRunRejectsInvalidConfigurationBeforePlatformChanges(t *testing.T) {
	system := &recordingPlatform{}
	err := run(guestconfig.Config{}, system, runtimePaths{})
	if err == nil || !strings.Contains(err.Error(), "validate workload configuration") {
		t.Fatalf("run() error = %v, want validation failure", err)
	}
	if len(system.calls) != 0 {
		t.Fatalf("platform calls = %q, want none", system.calls)
	}
}

func TestRunBuildsContainerEnvironmentBeforeExec(t *testing.T) {
	base := t.TempDir()
	rootfs := filepath.Join(base, "rootfs")
	workspace := filepath.Join(base, "workspace")
	for _, directory := range []string{rootfs, workspace} {
		if err := os.Mkdir(directory, 0o755); err != nil {
			t.Fatalf("create %s: %v", directory, err)
		}
	}
	system := &recordingPlatform{}
	config := guestconfig.Config{
		SchemaVersion: guestconfig.SchemaVersion,
		Process: guestconfig.Process{
			Args:             []string{"/bin/server", "--listen=:8080"},
			Env:              []string{"PATH=/bin", "MODE=production"},
			WorkingDirectory: "/srv/application",
			UID:              1000,
			GID:              1001,
		},
		Workspace: guestconfig.Workspace{
			Enabled:       true,
			ContainerPath: "/workspace",
		},
	}
	paths := runtimePaths{
		rootFS:    rootfs,
		workspace: workspace,
		dev:       "/host/dev",
		resolver:  "/host/resolv.conf",
	}

	if err := run(config, system, paths); err != nil {
		t.Fatalf("run(): %v", err)
	}
	wantCalls := []string{
		"lock-thread",
		"private",
		"null:/host/dev/null",
		"devices:/host/dev:" + filepath.Join(rootfs, "dev"),
		"proc:" + filepath.Join(rootfs, "proc"),
		"sys:" + filepath.Join(rootfs, "sys"),
		"bind:/host/resolv.conf:" + filepath.Join(rootfs, "etc", "resolv.conf"),
		"bind:" + workspace + ":" + filepath.Join(rootfs, "workspace"),
		"chroot:" + rootfs,
		"chdir:/srv/application",
		"restrict:1000:1001",
		"exec",
	}
	if !reflect.DeepEqual(system.calls, wantCalls) {
		t.Fatalf("platform calls = %#v, want %#v", system.calls, wantCalls)
	}
	if !reflect.DeepEqual(system.args, config.Process.Args) {
		t.Fatalf("args = %q, want %q", system.args, config.Process.Args)
	}
	if !reflect.DeepEqual(system.env, config.Process.Env) {
		t.Fatalf("env = %q, want %q", system.env, config.Process.Env)
	}
}

func TestRunBindsPlatformBunForCanonicalSDKWorkload(t *testing.T) {
	base := t.TempDir()
	rootfs := filepath.Join(base, "rootfs")
	if err := os.Mkdir(rootfs, 0o755); err != nil {
		t.Fatal(err)
	}
	platformBun := filepath.Join(base, "bun")
	if err := os.WriteFile(platformBun, []byte("runtime"), 0o755); err != nil {
		t.Fatal(err)
	}
	system := &recordingPlatform{}
	config := guestconfig.Config{
		SchemaVersion: guestconfig.SchemaVersion,
		Process: guestconfig.Process{
			Args:             []string{guestconfig.PlatformRuntimeContainerPath, "/opt/cantelop/session-runtime.mjs"},
			Env:              []string{"PATH=/bin"},
			WorkingDirectory: "/",
			UID:              1000,
			GID:              1000,
		},
	}
	paths := runtimePaths{
		rootFS: rootfs, dev: "/host/dev", resolver: "/host/resolv.conf", platformBun: platformBun,
	}
	if err := run(config, system, paths); err != nil {
		t.Fatalf("run(): %v", err)
	}
	want := "bind:" + platformBun + ":" + filepath.Join(rootfs, guestconfig.PlatformRuntimeContainerPath)
	if !slices.Contains(system.calls, want) {
		t.Fatalf("platform calls = %#v, missing %q", system.calls, want)
	}
	protected := "protect:" + filepath.Join(rootfs, guestconfig.PlatformRuntimeContainerPath)
	if !slices.Contains(system.calls, protected) {
		t.Fatalf("platform calls = %#v, missing %q", system.calls, protected)
	}
	wantArgs := []string{
		guestconfig.PlatformRuntimeContainerPath,
		platformBunNoInstallFlag,
		"/opt/cantelop/session-runtime.mjs",
	}
	if !reflect.DeepEqual(system.args, wantArgs) {
		t.Fatalf("exec args = %q, want %q", system.args, wantArgs)
	}
	// Versioned images use the same packaged loader as local Docker; the
	// platform still supplies and protects the actual Bun executable.
	config.Process.UID, config.Process.GID = guestconfig.SessionUID, guestconfig.SessionGID
	config.Process.WorkingDirectory = guestconfig.SessionWorkspace
	config.Process.Env = guestconfig.SessionEnvironment()
	if err := run(config, system, paths); err != nil {
		t.Fatal(err)
	}
	wantArgs = append([]string{guestconfig.SessionLoader, "--library-path", guestconfig.SessionLibraryPath}, wantArgs...)
	if !reflect.DeepEqual(system.args, wantArgs) {
		t.Fatalf("managed exec args = %q, want %q", system.args, wantArgs)
	}
}

func TestRecordingPlatformFailureIsReturned(t *testing.T) {
	system := &recordingPlatform{failCall: "private"}
	err := system.privateMountNamespace()
	if !errors.Is(err, errRecordingPlatform) {
		t.Fatalf("privateMountNamespace() error = %v", err)
	}
}

func TestProcessConfigurationIsPassedWithoutRewriting(t *testing.T) {
	system := &recordingPlatform{}
	args := []string{"/bin/server", "--listen=:8080"}
	env := []string{"PATH=/bin", "MODE=production"}
	if err := system.exec(args, env); err != nil {
		t.Fatalf("exec(): %v", err)
	}
	if !reflect.DeepEqual(system.args, args) {
		t.Fatalf("args = %q, want %q", system.args, args)
	}
	if !reflect.DeepEqual(system.env, env) {
		t.Fatalf("env = %q, want %q", system.env, env)
	}
}

var errRecordingPlatform = errors.New("recording platform failure")

type recordingPlatform struct {
	calls    []string
	failCall string
	args     []string
	env      []string
}

func (p *recordingPlatform) record(call string) error {
	p.calls = append(p.calls, call)
	if p.failCall == call {
		return errRecordingPlatform
	}
	return nil
}

func (p *recordingPlatform) lockOSThread() {
	p.calls = append(p.calls, "lock-thread")
}

func (p *recordingPlatform) privateMountNamespace() error {
	return p.record("private")
}

func (p *recordingPlatform) ensureNullDevice(path string) error {
	return p.record("null:" + path)
}

func (p *recordingPlatform) mountMinimalDevices(source, target string) error {
	return p.record("devices:" + source + ":" + target)
}

func (p *recordingPlatform) bindMount(source, target string, recursive bool) error {
	return p.record("bind:" + source + ":" + target)
}

func (p *recordingPlatform) protectRuntimeMount(target string) error {
	return p.record("protect:" + target)
}

func (p *recordingPlatform) mountProc(target string) error {
	return p.record("proc:" + target)
}

func (p *recordingPlatform) mountSys(target string) error {
	return p.record("sys:" + target)
}

func (p *recordingPlatform) chroot(path string) error {
	return p.record("chroot:" + path)
}

func (p *recordingPlatform) chdir(path string) error {
	return p.record("chdir:" + path)
}

func (p *recordingPlatform) restrictPrivileges(uid, gid uint32) error {
	return p.record("restrict:" + strconv.FormatUint(uint64(uid), 10) + ":" + strconv.FormatUint(uint64(gid), 10))
}

func (p *recordingPlatform) exec(args, env []string) error {
	p.args = append([]string(nil), args...)
	p.env = append([]string(nil), env...)
	return p.record("exec")
}
