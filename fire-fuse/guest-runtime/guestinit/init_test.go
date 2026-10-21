package guestinit

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fakePlatform struct {
	calls   []string
	failAt  string
	failErr error
}

func (f *fakePlatform) record(call string) error {
	f.calls = append(f.calls, call)
	if f.failAt == call {
		return f.failErr
	}
	return nil
}

func (f *fakePlatform) mkdirAll(path string, mode os.FileMode) error {
	return f.record(fmt.Sprintf("mkdir %s %o", path, mode))
}

func (f *fakePlatform) mountDev(target string) error {
	return f.record("mount-dev " + target)
}

func (f *fakePlatform) mountProc(target string) error {
	return f.record("mount-proc " + target)
}

func (f *fakePlatform) mountSys(target string) error {
	return f.record("mount-sys " + target)
}

func (f *fakePlatform) waitForBlockDevice(path string, timeout time.Duration) error {
	return f.record(fmt.Sprintf("wait-block %s %s", path, timeout))
}

func (f *fakePlatform) mountLower(device, target string) error {
	return f.record("mount-lower " + device + " " + target)
}

func (f *fakePlatform) mountUpper(target, size string) error {
	return f.record("mount-upper " + target + " " + size)
}

func (f *fakePlatform) mountOverlay(lower, upper, work, target string) error {
	return f.record(
		"mount-overlay " + lower + " " + upper + " " + work + " " + target,
	)
}

func (f *fakePlatform) moveMount(source, target string) error {
	return f.record("move " + source + " " + target)
}

func (f *fakePlatform) switchRoot(path string) error {
	return f.record("switch-root " + path)
}

func (f *fakePlatform) exec(path string, args, env []string) error {
	return f.record(
		"exec " + path + " " + strings.Join(args, ",") + " " + strings.Join(env, ","),
	)
}

func TestRunBuildsEphemeralPlatformRootBeforeExecutingInit(t *testing.T) {
	wantExec := errors.New("exec intercepted")
	system := &fakePlatform{
		failAt:  "exec /sbin/init /sbin/init HOME=/,TERM=linux,PATH=" + platformInitPath,
		failErr: wantExec,
	}

	err := run(system)
	if !errors.Is(err, wantExec) {
		t.Fatalf("run() error = %v, want exec error", err)
	}

	want := []string{
		"mkdir /dev 755",
		"mkdir /proc 755",
		"mkdir /sys 755",
		"mkdir /run/fire-fuse/platform-lower 755",
		"mkdir /run/fire-fuse/platform-overlay 755",
		"mkdir /run/fire-fuse/platform-root 755",
		"mount-dev /dev",
		"mount-proc /proc",
		"mount-sys /sys",
		"wait-block /dev/vda 5s",
		"mount-lower /dev/vda /run/fire-fuse/platform-lower",
		"mount-upper /run/fire-fuse/platform-overlay 128m",
		"mkdir /run/fire-fuse/platform-overlay/upper 755",
		"mkdir /run/fire-fuse/platform-overlay/work 755",
		"mount-overlay /run/fire-fuse/platform-lower /run/fire-fuse/platform-overlay/upper /run/fire-fuse/platform-overlay/work /run/fire-fuse/platform-root",
		"mkdir /run/fire-fuse/platform-root/dev 755",
		"mkdir /run/fire-fuse/platform-root/proc 755",
		"mkdir /run/fire-fuse/platform-root/sys 755",
		"move /dev /run/fire-fuse/platform-root/dev",
		"move /proc /run/fire-fuse/platform-root/proc",
		"move /sys /run/fire-fuse/platform-root/sys",
		"switch-root /run/fire-fuse/platform-root",
		"exec /sbin/init /sbin/init HOME=/,TERM=linux,PATH=" + platformInitPath,
	}
	if !reflect.DeepEqual(system.calls, want) {
		t.Errorf("calls = %#v, want %#v", system.calls, want)
	}
}

func TestRunStopsAtFirstMountFailure(t *testing.T) {
	wantErr := errors.New("lower mount failed")
	system := &fakePlatform{
		failAt:  "mount-lower /dev/vda /run/fire-fuse/platform-lower",
		failErr: wantErr,
	}

	err := run(system)
	if !errors.Is(err, wantErr) {
		t.Fatalf("run() error = %v, want lower mount error", err)
	}
	if !strings.Contains(err.Error(), "mount platform lower") {
		t.Errorf("run() error = %v, want operation context", err)
	}
	for _, call := range system.calls {
		if strings.HasPrefix(call, "mount-upper ") {
			t.Errorf("upper mount attempted after lower failure: %#v", system.calls)
		}
	}
}
