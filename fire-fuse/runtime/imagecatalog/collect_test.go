package imagecatalog

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stepandel/cantelop/fire-fuse/config"
	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
)

func TestValidateRootFSPathRequiresExactContentAddressedLocation(t *testing.T) {
	digest := strings.Repeat("a", 64)
	valid := imageRoot + "/sha256-" + digest + ".ext4"
	got, err := validateRootFSPath(valid)
	if err != nil || got != digest {
		t.Fatalf("validateRootFSPath() = %q, %v; want %q", got, err, digest)
	}
	for _, path := range []string{
		"relative.ext4",
		imageRoot + "/../images/sha256-" + digest + ".ext4",
		imageRoot + "/nested/sha256-" + digest + ".ext4",
		imageRoot + "/" + digest + ".ext4",
		imageRoot + "/sha256-" + strings.Repeat("A", 64) + ".ext4",
	} {
		if _, err := validateRootFSPath(path); err == nil {
			t.Errorf("validateRootFSPath(%q) succeeded", path)
		}
	}
}

func TestEnsureRootFSUnreferencedRejectsEveryPublishedReference(t *testing.T) {
	rootfsPath := testImage().RootFSImage
	otherProcess := testImage()
	otherProcess.Process.Args = []string{"/bin/other"}
	otherID, err := config.ContainerImageID(otherProcess)
	if err != nil {
		t.Fatalf("ContainerImageID() error = %v", err)
	}
	err = ensureRootFSUnreferenced(map[string]config.ContainerImage{
		testImageID: testImage(),
		otherID:     otherProcess,
	}, nil, nil, rootfsPath)
	if !errors.Is(err, ErrRootFSInUse) || !strings.Contains(err.Error(), testImageID) || !strings.Contains(err.Error(), otherID) {
		t.Fatalf("ensureRootFSUnreferenced() error = %v, want both published references", err)
	}
}

func TestEnsureRootFSUnreferencedRejectsUnresolvedLiveSandbox(t *testing.T) {
	err := ensureRootFSUnreferenced(map[string]config.ContainerImage{}, nil, []microvm.Sandbox{
		{ID: "vm-terminated", BuildID: testImageID, State: microvm.StateTerminated},
		{ID: "vm-running", BuildID: testImageID, State: microvm.StateRunning},
	}, testImage().RootFSImage)
	if !errors.Is(err, ErrUnresolvedActiveImage) || !strings.Contains(err.Error(), "vm-running") {
		t.Fatalf("ensureRootFSUnreferenced() error = %v, want unresolved running VM", err)
	}
	if strings.Contains(err.Error(), "vm-terminated") {
		t.Fatalf("terminated VM unexpectedly blocks collection: %v", err)
	}
}

func TestEnsureRootFSUnreferencedRejectsUnpublishedRegisteredBuild(t *testing.T) {
	err := ensureRootFSUnreferenced(
		map[string]config.ContainerImage{},
		[]microvm.AppBuild{{AppID: "runtime", BuildID: testImageID}},
		nil,
		testImage().RootFSImage,
	)
	if !errors.Is(err, ErrUnresolvedAppBuild) || !strings.Contains(err.Error(), "runtime/"+testImageID) {
		t.Fatalf("ensureRootFSUnreferenced() error = %v, want unresolved registered build", err)
	}
}

func TestEnsureRootFSUnreferencedAllowsResolvedLiveVMOnOtherRootFS(t *testing.T) {
	image := testImage()
	image.RootFSImage = imageRoot + "/sha256-" + strings.Repeat("b", 64) + ".ext4"
	if err := ensureRootFSUnreferenced(map[string]config.ContainerImage{
		testImageID: image,
	}, []microvm.AppBuild{{AppID: "runtime", BuildID: testImageID}}, []microvm.Sandbox{{
		ID: "vm-running", BuildID: testImageID, State: microvm.StateRunning,
	}}, testImage().RootFSImage); err != nil {
		t.Fatalf("ensureRootFSUnreferenced() error = %v", err)
	}
}

func TestVerifyFileDigest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rootfs.ext4")
	if err := os.WriteFile(path, []byte("rootfs"), 0o600); err != nil {
		t.Fatalf("write rootfs: %v", err)
	}
	if err := verifyFileDigest(path, "3c47ef972d531d524daa15fa33dd885dd23de6221bbd10a29eb42ecfcf2ef422"); err != nil {
		t.Fatalf("verifyFileDigest() error = %v", err)
	}
	if err := verifyFileDigest(path, strings.Repeat("0", 64)); err == nil {
		t.Fatal("verifyFileDigest() accepted the wrong digest")
	}
}
