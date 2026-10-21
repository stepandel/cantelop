package materializer

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/config"
	"github.com/stepandel/cantelop/fire-fuse/guest-runtime/guestconfig"
)

func TestUploadedSessionImagesRequireManagedRuntimeContract(t *testing.T) {
	for _, test := range []struct {
		name      string
		env       []string
		volumes   map[string]any
		wantError bool
	}{
		{"managed", guestconfig.SessionEnvironment(), nil, false},
		{"old-cli", nil, nil, true},
		{"volume", guestconfig.SessionEnvironment(), map[string]any{"/data": map[string]any{}}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			document, _ := json.Marshal([]any{map[string]any{"Architecture": "amd64", "OS": "linux", "Config": map[string]any{
				"Cmd": []string{guestconfig.PlatformRuntimeContainerPath, guestconfig.SessionModule},
				"Env": test.env, "Volumes": test.volumes, "WorkingDir": "/workspace", "User": "10001:10001",
			}}})
			_, _, err := decodeDockerImage(document)
			if (err != nil) != test.wantError {
				t.Fatalf("decodeDockerImage() = %v", err)
			}
		})
	}
}

type publisherFunc func(config.ContainerImage) (string, error)

func (f publisherFunc) Publish(image config.ContainerImage) (string, error) { return f(image) }

type runnerFunc func(context.Context, []string, string, ...string) ([]byte, error)

func (f runnerFunc) Run(ctx context.Context, environment []string, name string, arguments ...string) ([]byte, error) {
	return f(ctx, environment, name, arguments...)
}

func TestMaterializePublishesSingleDockerArchiveImage(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "image.tar")
	writeDockerArchive(t, archivePath, false)
	work, images := filepath.Join(t.TempDir(), "work"), filepath.Join(t.TempDir(), "images")
	wantBuildID := "sha256:" + strings.Repeat("b", 64)
	wantLoadedImageID := "sha256:" + fmt.Sprintf("%x", sha256.Sum256([]byte(`{"architecture":"amd64"}`)))
	imageRemoved := false
	var preparedImages []config.ContainerImage
	materializer, err := New("/docker", "/mkfs.ext4", work, images, publisherFunc(func(image config.ContainerImage) (string, error) {
		if image.Process.UID != 1000 || image.Process.GID != 1000 || len(image.Process.Args) != 2 || image.Process.Args[0] != "/app/server" {
			t.Fatalf("process = %#v", image.Process)
		}
		if !strings.HasPrefix(image.RootFSImage, images+"/sha256-") || image.RootFSSizeBytes == 0 {
			t.Fatalf("rootfs = %#v", image)
		}
		preparedImages = append(preparedImages, image)
		return wantBuildID, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	materializer.runner = runnerFunc(func(_ context.Context, environment []string, name string, arguments ...string) ([]byte, error) {
		switch {
		case name == "/docker" && len(arguments) >= 2 && arguments[0] == "image" && arguments[1] == "load":
			return []byte("Loaded image\n"), nil
		case name == "/docker" && len(arguments) >= 2 && arguments[0] == "image" && arguments[1] == "inspect":
			return []byte(`[{"Architecture":"amd64","Os":"linux","Config":{"Entrypoint":["/app/server"],"Cmd":["serve"],"Env":["PATH=/usr/bin:/bin"],"WorkingDir":"/app","User":"1000"}}]`), nil
		case name == "/docker" && len(arguments) == 4 && arguments[0] == "image" && arguments[1] == "rm":
			imageRemoved = arguments[2] == "--force" && arguments[3] == wantLoadedImageID
			return nil, nil
		case name == "/docker" && arguments[0] == "create":
			return []byte(strings.Repeat("c", 64) + "\n"), nil
		case name == "/docker" && arguments[0] == "export":
			writeExportedRootFS(t, arguments[2])
			return nil, nil
		case name == "/docker" && arguments[0] == "rm":
			return nil, nil
		case name == "/mkfs.ext4":
			if len(environment) != 1 || environment[0] != "E2FSPROGS_FAKE_TIME=946684800" || !strings.Contains(strings.Join(arguments, " "), "lazy_itable_init=0") {
				t.Fatalf("deterministic mkfs environment/arguments = %v %v", environment, arguments)
			}
			root := arguments[len(arguments)-2]
			if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				if info.ModTime() != time.Unix(deterministicBuildUnixSecond, 0) {
					t.Errorf("rootfs timestamp for %s = %s", path, info.ModTime())
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			return nil, os.WriteFile(arguments[len(arguments)-1], []byte("deterministic-ext4"), 0o644)
		default:
			return nil, fmt.Errorf("unexpected command %s %v", name, arguments)
		}
	})
	buildID, err := materializer.Materialize(context.Background(), "bop_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", archivePath)
	if err != nil || buildID != wantBuildID {
		t.Fatalf("Materialize() = %q, %v", buildID, err)
	}
	if !imageRemoved {
		t.Fatal("loaded Docker image was not removed")
	}
	secondBuildID, err := materializer.Materialize(context.Background(), "bop_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", archivePath)
	if err != nil || secondBuildID != buildID {
		t.Fatalf("second Materialize() = %q, %v", secondBuildID, err)
	}
	if len(preparedImages) != 2 || preparedImages[0].RootFSImage == preparedImages[1].RootFSImage ||
		preparedImages[0].RootFSSHA256 != preparedImages[1].RootFSSHA256 {
		t.Fatalf("equal-content conversions did not get distinct owned paths: %#v", preparedImages)
	}
	if err := os.Remove(preparedImages[0].RootFSImage); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(preparedImages[1].RootFSImage); err != nil {
		t.Fatalf("peer image was removed by cleanup: %v", err)
	}
}

func TestRootFSImageSizeCountsHardLinkedDataOnce(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.WriteFile(source, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(source, 200<<20); err != nil {
		t.Fatal(err)
	}
	for index := range 320 {
		if err := os.Link(source, filepath.Join(root, fmt.Sprintf("link-%03d", index))); err != nil {
			t.Fatal(err)
		}
	}
	got, err := rootFSImageSize(root)
	if err != nil || got != 315<<20 {
		t.Fatalf("rootFSImageSize() with hard links = %d, %v; want %d", got, err, int64(315<<20))
	}
	other := filepath.Join(root, "other")
	if err := os.WriteFile(other, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(other, 200<<20); err != nil {
		t.Fatal(err)
	}
	got, err = rootFSImageSize(root)
	if err != nil || got != 565<<20 {
		t.Fatalf("rootFSImageSize() with distinct files = %d, %v; want %d", got, err, int64(565<<20))
	}
}

func TestRootFSImageSizeRejectsOversizedImage(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "source"), bytes.Repeat([]byte("a"), 1<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := rootFSImageSizeWithLimit(root, 64<<20); err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("rootFSImageSizeWithLimit() error = %v; want size limit error", err)
	}
}

func TestExtractRootFSCreatesHardLinkChains(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "rootfs.tar")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := tar.NewWriter(file)
	content := bytes.Repeat([]byte("a"), 1<<20)
	if err := writer.WriteHeader(&tar.Header{Name: "file", Mode: 0o600, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(content); err != nil {
		t.Fatal(err)
	}
	for _, link := range []struct{ name, target string }{{"link-1", "file"}, {"link-2", "link-1"}} {
		if err := writer.WriteHeader(&tar.Header{Name: link.name, Linkname: link.target, Mode: 0o600, Typeflag: tar.TypeLink}); err != nil {
			t.Fatal(err)
		}
	}
	if err := errors.Join(writer.Close(), file.Close()); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := extractRootFS(context.Background(), archivePath, root); err != nil {
		t.Fatalf("extractRootFS() rejected valid links: %v", err)
	}
	if info, err := os.Stat(filepath.Join(root, "link-2")); err != nil || info.Size() != 1<<20 {
		t.Fatalf("last valid hard link = %v, %v", info, err)
	}
}

func TestInstallRootFSRemovesOwnedLinkWhenSyncFails(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "source.ext4")
	destination := filepath.Join(directory, "sha256-owned.ext4")
	if err := os.WriteFile(source, []byte("image"), 0o600); err != nil {
		t.Fatal(err)
	}
	want := errors.New("sync failed")
	if err := installRootFSWithSync(source, destination, func(string) error { return want }); !errors.Is(err, want) {
		t.Fatalf("installRootFSWithSync() = %v", err)
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed image link remains: %v", err)
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("source image was removed: %v", err)
	}
}

func TestInspectDockerArchiveRejectsTraversal(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "image.tar")
	writeDockerArchive(t, archivePath, true)
	if _, err := inspectDockerArchive(archivePath); err == nil {
		t.Fatal("unsafe Docker archive accepted")
	}
}

func TestInspectDockerArchiveAcceptsOCIImageSaveLayout(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "image.tar")
	configuration := []byte(`{"architecture":"amd64","os":"linux"}`)
	configurationDigest := fmt.Sprintf("%x", sha256.Sum256(configuration))
	layer := []byte("layer contents")
	layerDigest := fmt.Sprintf("%x", sha256.Sum256(layer))
	ociManifest, _ := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"config":        map[string]any{"digest": "sha256:" + configurationDigest},
		"layers":        []map[string]any{{"digest": "sha256:" + layerDigest}},
	})
	ociManifestDigest := fmt.Sprintf("%x", sha256.Sum256(ociManifest))
	index, _ := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"manifests": []map[string]any{{
			"digest": "sha256:" + ociManifestDigest,
			"size":   len(ociManifest),
		}},
	})
	manifest, _ := json.Marshal([]dockerArchiveManifest{{
		Config: "blobs/sha256/" + configurationDigest,
		Layers: []string{"blobs/sha256/" + layerDigest},
	}})
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := tar.NewWriter(file)
	for name, document := range map[string][]byte{
		"manifest.json":                       manifest,
		"index.json":                          index,
		"blobs/sha256/" + configurationDigest: configuration,
		"blobs/sha256/" + layerDigest:         layer,
		"blobs/sha256/" + ociManifestDigest:   ociManifest,
	} {
		if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(document)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = writer.Write(document)
	}
	_ = writer.Close()
	_ = file.Close()

	imageID, err := inspectDockerArchive(archivePath)
	if err != nil || imageID != "sha256:"+ociManifestDigest {
		t.Fatalf("inspectDockerArchive() = %q, %v", imageID, err)
	}
}

func TestInspectDockerArchiveRejectsRepositoryTags(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "image.tar")
	manifest, _ := json.Marshal([]dockerArchiveManifest{{
		Config:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.json",
		RepoTags: []string{"trusted/platform:latest"}, Layers: []string{"layer/layer.tar"},
	}})
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := tar.NewWriter(file)
	if err := writer.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0o600, Size: int64(len(manifest)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	_, _ = writer.Write(manifest)
	_ = writer.Close()
	_ = file.Close()
	if _, err := inspectDockerArchive(archivePath); err == nil || !strings.Contains(err.Error(), "repository tags") {
		t.Fatalf("inspectDockerArchive() error = %v", err)
	}
}

func TestExtractRootFSRejectsWritesThroughSymlinkParent(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "rootfs.tar")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := tar.NewWriter(file)
	_ = writer.WriteHeader(&tar.Header{Name: "escape", Typeflag: tar.TypeSymlink, Linkname: t.TempDir(), Mode: 0o777})
	_ = writer.WriteHeader(&tar.Header{Name: "escape/payload", Typeflag: tar.TypeReg, Mode: 0o600, Size: 1})
	_, _ = writer.Write([]byte("x"))
	_ = writer.Close()
	_ = file.Close()
	root := filepath.Join(t.TempDir(), "root")
	_ = os.Mkdir(root, 0o700)
	if err := extractRootFS(context.Background(), archivePath, root); err == nil {
		t.Fatal("rootfs extractor followed a symlink parent")
	}
}

func TestExtractRootFSRejectsExpandedByteBudget(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "rootfs.tar")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := tar.NewWriter(file)
	if err := writer.WriteHeader(&tar.Header{Name: "oversized", Mode: 0o600, Size: maxExtractedRootFSBytes + 1, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	_ = file.Close()
	root := t.TempDir()
	if err := extractRootFS(context.Background(), archivePath, root); err == nil || !strings.Contains(err.Error(), "expanded byte limit") {
		t.Fatalf("extractRootFS() error = %v", err)
	}
}

func TestExtractRootFSPreservesExecutableModeWithRestrictiveUmask(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "rootfs.tar")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := tar.NewWriter(file)
	_ = writer.WriteHeader(&tar.Header{Name: "server", Typeflag: tar.TypeReg, Mode: 0o755, Size: 1})
	_, _ = writer.Write([]byte("x"))
	_ = writer.Close()
	_ = file.Close()
	root := filepath.Join(t.TempDir(), "root")
	_ = os.Mkdir(root, 0o700)
	oldUmask := syscall.Umask(0o077)
	t.Cleanup(func() { syscall.Umask(oldUmask) })
	if err := extractRootFS(context.Background(), archivePath, root); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(root, "server"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("extracted mode = %o, want 755", info.Mode().Perm())
	}
}

func TestResolveProcessIdentityReadsAccountFilesInsideRootFS(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "etc"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "etc", "passwd"), []byte("app:x:1000:1001::/:/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "etc", "group"), []byte("app:x:1002:\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	uid, gid, err := resolveProcessIdentity(root, "app:app")
	if err != nil || uid != 1000 || gid != 1002 {
		t.Fatalf("resolveProcessIdentity() = %d, %d, %v", uid, gid, err)
	}
}

func TestResolveProcessIdentityRejectsSymlinkedAccountFile(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "etc"), 0o700); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "passwd")
	if err := os.WriteFile(external, []byte("host:x:0:0::/:/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(root, "etc", "passwd")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolveProcessIdentity(root, "host"); err == nil {
		t.Fatal("resolveProcessIdentity accepted a symlinked passwd file")
	}
}

func writeDockerArchive(t *testing.T, path string, traversal bool) {
	t.Helper()
	configuration := []byte(`{"architecture":"amd64"}`)
	digest := fmt.Sprintf("%x", sha256.Sum256(configuration))
	manifest, _ := json.Marshal([]dockerArchiveManifest{{Config: digest + ".json", Layers: []string{"layer/layer.tar"}}})
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := tar.NewWriter(file)
	_ = writer.WriteHeader(&tar.Header{Name: "layer/", Mode: 0o755, Typeflag: tar.TypeDir})
	for name, document := range map[string][]byte{
		"manifest.json":   manifest,
		digest + ".json":  configuration,
		"layer/layer.tar": {},
	} {
		if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(document)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = writer.Write(document)
	}
	if traversal {
		_ = writer.WriteHeader(&tar.Header{Name: "../escape", Mode: 0o600, Typeflag: tar.TypeReg})
	}
	_ = writer.Close()
	_ = file.Close()
}

func writeExportedRootFS(t *testing.T, path string) {
	t.Helper()
	var document bytes.Buffer
	writer := tar.NewWriter(&document)
	for _, entry := range []struct {
		name string
		body string
		mode int64
	}{
		{"app", "", 0o755},
		{"app/server", "binary", 0o755},
	} {
		typeflag := byte(tar.TypeReg)
		if entry.body == "" {
			typeflag = tar.TypeDir
		}
		_ = writer.WriteHeader(&tar.Header{Name: entry.name, Typeflag: typeflag, Mode: entry.mode, Size: int64(len(entry.body)), Uid: 1000, Gid: 1000})
		_, _ = writer.Write([]byte(entry.body))
	}
	_ = writer.Close()
	if err := os.WriteFile(path, document.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}
