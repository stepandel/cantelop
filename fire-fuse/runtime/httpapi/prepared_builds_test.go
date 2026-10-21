package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/config"
	"github.com/stepandel/cantelop/fire-fuse/guest-runtime/guestconfig"
	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
)

type preparedTransport struct{ document []byte }

func (t preparedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(t.document)),
		Header: make(http.Header), ContentLength: int64(len(t.document)), Request: request,
	}, nil
}

func TestPreparedBuildCommandValidatesIdentityAndOrigin(t *testing.T) {
	root := t.TempDir()
	digest := strings.Repeat("a", 64)
	process := guestconfig.Process{Args: []string{"/app/server"}, Env: []string{"PATH=/usr/bin"}, WorkingDirectory: "/app", UID: 10001, GID: 10001}
	image := config.ContainerImage{RootFSImage: filepath.Join(root, "sha256-"+digest+".ext4"), RootFSSHA256: digest, RootFSSizeBytes: 4096, Process: process}
	buildID, err := config.ContainerImageID(image)
	if err != nil {
		t.Fatal(err)
	}
	command := preparedCommand{
		CommandID: "op_0123456789abcdef0123456789abcdef",
		ImageURL:  "https://r2.example.test/bucket/prepared-images/sha256/" + digest + ".ext4?X-Amz-Signature=signed",
		Manifest:  preparedManifest{SchemaVersion: 1, BuildID: buildID, ImageKey: "prepared-images/sha256/" + digest + ".ext4", RootFSSHA256: digest, RootFSSizeBytes: 4096, Process: process},
	}
	if err := command.validate("https://r2.example.test", root); err != nil {
		t.Fatalf("valid prepared command: %v", err)
	}
	command.ImageURL = "https://metadata.invalid/bucket/prepared-images/sha256/" + digest + ".ext4?sig=x"
	if err := command.validate("https://r2.example.test", root); err == nil {
		t.Fatal("untrusted artifact origin was accepted")
	}
	command.ImageURL = "https://r2.example.test/bucket/prepared-images/sha256/" + digest + ".ext4?sig=x"
	command.Manifest.BuildID = "sha256:" + strings.Repeat("b", 64)
	if err := command.validate("https://r2.example.test", root); err == nil {
		t.Fatal("incorrect Build ID was accepted")
	}
}

func TestPreparedInstallerVerifiesExactImageBeforeLink(t *testing.T) {
	root := t.TempDir()
	document := []byte("prepared ext4 fixture")
	hash := sha256.Sum256(document)
	digest := hex.EncodeToString(hash[:])
	image := config.ContainerImage{RootFSImage: filepath.Join(root, "sha256-"+digest+".ext4"), RootFSSHA256: digest, RootFSSizeBytes: int64(len(document))}
	h := preparedBuildIngress{imageDirectory: root, client: &http.Client{Transport: preparedTransport{document: document}}, statFilesystem: plentyOfDisk}
	if err := h.downloadAndInstall(context.Background(), "https://r2.example.test/image?sig=x", image); err != nil {
		t.Fatal(err)
	}
	installed, err := os.ReadFile(image.RootFSImage)
	if err != nil || !bytes.Equal(installed, document) {
		t.Fatalf("installed image = %q, %v", installed, err)
	}
	bad := []byte("tampered ext4 fixture")
	h.client = &http.Client{Transport: preparedTransport{document: bad}}
	if err := h.downloadAndInstall(context.Background(), "https://r2.example.test/image?sig=x", image); err == nil {
		t.Fatal("tampered prepared image was accepted")
	}
}

func plentyOfDisk(string) (uint64, uint64, error) { return 1 << 40, 1 << 40, nil }

func TestPreparedInstallerKeepsImageDiskReserve(t *testing.T) {
	root := t.TempDir()
	document := []byte("prepared ext4 fixture")
	hash := sha256.Sum256(document)
	image := config.ContainerImage{RootFSImage: filepath.Join(root, "sha256-"+hex.EncodeToString(hash[:])+".ext4"), RootFSSHA256: hex.EncodeToString(hash[:]), RootFSSizeBytes: int64(len(document))}
	var transport countingTransport
	transport.document = document
	for _, disk := range []struct{ available, total uint64 }{
		{available: 4 << 30, total: 40 << 30},    // below the 4 GiB floor once the image lands
		{available: 20 << 30, total: 1000 << 30}, // below 5% of a large disk
		{available: 10, total: 1 << 40},          // smaller than the image itself
	} {
		h := preparedBuildIngress{imageDirectory: root, client: &http.Client{Transport: &transport},
			statFilesystem: func(string) (uint64, uint64, error) { return disk.available, disk.total, nil }}
		if err := h.downloadAndInstall(context.Background(), "https://r2.example.test/image?sig=x", image); !errors.Is(err, errImageDiskCapacity) {
			t.Fatalf("available=%d total=%d: %v", disk.available, disk.total, err)
		}
	}
	if transport.calls != 0 {
		t.Fatalf("download started %d times without disk headroom", transport.calls)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("image directory = %v, %v", entries, err)
	}
	h := preparedBuildIngress{imageDirectory: root, client: &http.Client{Transport: &transport},
		statFilesystem: func(string) (uint64, uint64, error) { return 5 << 30, 40 << 30, nil }}
	if err := h.downloadAndInstall(context.Background(), "https://r2.example.test/image?sig=x", image); err != nil {
		t.Fatalf("install with headroom: %v", err)
	}
}

type countingTransport struct {
	preparedTransport
	calls int
}

func (t *countingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.calls++
	return t.preparedTransport.RoundTrip(request)
}

func TestPreparedIngressRemovesAbandonedDownloads(t *testing.T) {
	root := t.TempDir()
	write := func(name string) string {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	abandoned := write(".prepared-123.ext4")
	installed := write("sha256-" + strings.Repeat("a", 64) + ".ext4")
	other := write(".prepared-123.json")
	if err := os.Mkdir(filepath.Join(root, ".prepared-dir.ext4"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(installed, filepath.Join(root, ".prepared-link.ext4")); err != nil {
		t.Fatal(err)
	}
	if _, err := NewPreparedBuildIngressHandler(microvm.NewService(nil), "https://r2.example.test", root, filepath.Join(t.TempDir(), "catalog.json"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(abandoned); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("abandoned download remains: %v", err)
	}
	for _, kept := range []string{installed, other, filepath.Join(root, ".prepared-dir.ext4"), filepath.Join(root, ".prepared-link.ext4")} {
		if _, err := os.Lstat(kept); err != nil {
			t.Fatalf("%s removed: %v", kept, err)
		}
	}
	if _, err := NewPreparedBuildIngressHandler(microvm.NewService(nil), "https://r2.example.test", filepath.Join(root, "missing"), filepath.Join(t.TempDir(), "catalog.json"), nil); err != nil {
		t.Fatalf("missing image directory: %v", err)
	}
}

func TestProviderIngressExposesOnlyPreparedBuildEndpoint(t *testing.T) {
	service := microvm.NewService(newTestStore(t))
	handler, err := NewPreparedBuildIngressHandler(service, "https://r2.example.test", t.TempDir(), filepath.Join(t.TempDir(), "catalog.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/v1/internal/users/usr_test/apps/app_test/build-materializations",
		"/v1/internal/users/usr_test/apps/app_test/builds",
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("legacy endpoint %s returned %d", path, recorder.Code)
		}
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("health returned %d", recorder.Code)
	}
}

func TestPreparedIngressReleaseAdmissionIsScopedAndGenerationChecked(t *testing.T) {
	store := newTestStore(t)
	service := microvm.NewService(store)
	_, err := store.EnsureComputeTenant(microvm.ComputeTenant{ID: "usr_test", CreatedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.EnsureComputeTenant(microvm.ComputeTenant{ID: "usr_other", CreatedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	if err := store.CreateProvisioningAppForComputeTenant("usr_test", microvm.App{ID: "app-test", Slug: "test", CreatedAt: at, UpdatedAt: at}, microvm.AppSessionRuntime{Runtime: microvm.AppRuntimeSpec{Profile: microvm.DefaultSandboxProfileName, InternalPort: 3000}}); err != nil {
		t.Fatal(err)
	}
	handler, err := NewPreparedBuildIngressHandler(service, "https://r2.example.test", t.TempDir(), filepath.Join(t.TempDir(), "catalog.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	path := "/v1/internal/users/usr_test/apps/app-test/release-admission"
	initial := httptest.NewRecorder()
	handler.ServeHTTP(initial, httptest.NewRequest(http.MethodGet, path, nil))
	if initial.Code != http.StatusOK || !strings.Contains(initial.Body.String(), `"closed":true`) {
		t.Fatalf("new App must begin closed: %d %s", initial.Code, initial.Body.String())
	}
	build := httptest.NewRecorder()
	handler.ServeHTTP(build, httptest.NewRequest(http.MethodGet,
		"/v1/internal/users/usr_test/apps/app-test/active-build", nil))
	if build.Code != http.StatusOK || !strings.Contains(build.Body.String(), `"build_id":null`) {
		t.Fatalf("active Build readback = %d %s", build.Code, build.Body.String())
	}
	put := func(target, body string) int {
		t.Helper()
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPut, target, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(recorder, request)
		return recorder.Code
	}
	gen := "act_0123456789abcdef0123456789abcdef"
	if got := put(strings.Replace(path, "usr_test", "usr_other", 1), `{"generation":"`+gen+`","closed":true}`); got != http.StatusNotFound {
		t.Fatalf("cross-tenant gate update = %d", got)
	}
	if got := put(path, `{"generation":"`+gen+`","closed":true}`); got != http.StatusOK {
		t.Fatalf("close gate = %d", got)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"closed":true`) {
		t.Fatalf("gate readback = %d %s", recorder.Code, recorder.Body.String())
	}
	if got := put(path, `{"generation":"act_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","closed":false}`); got != http.StatusConflict {
		t.Fatalf("wrong-generation open = %d", got)
	}
	if got := put(path, `{"generation":"`+gen+`","closed":false}`); got != http.StatusOK {
		t.Fatalf("open gate = %d", got)
	}
}

func TestActiveBuildSelectionRejectsMalformedOrUnknownTargets(t *testing.T) {
	service := microvm.NewService(newTestStore(t))
	handler, err := NewPreparedBuildIngressHandler(service, "https://r2.example.test", t.TempDir(), filepath.Join(t.TempDir(), "catalog.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	path := "/v1/internal/users/usr_test/apps/app_test/active-build"
	for _, body := range []string{`{"build_id":"sha256:invalid"}`, `{"build_id":"sha256:` + strings.Repeat("a", 64) + `","extra":true}`, `{}`} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPut, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("invalid body %s returned %d", body, recorder.Code)
		}
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, path, strings.NewReader(`{"build_id":"sha256:`+strings.Repeat("a", 64)+`"}`))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unknown App returned %d", recorder.Code)
	}
}

func TestPreparedIngressReportsWhetherAppStillRetainsBuild(t *testing.T) {
	store := newTestStore(t)
	at := time.Now().UTC()
	for _, tenant := range []string{"usr_test", "usr_other"} {
		if _, err := store.EnsureComputeTenant(microvm.ComputeTenant{ID: tenant, CreatedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	retained := "sha256:" + strings.Repeat("a", 64)
	if err := store.CreateAppForComputeTenant("usr_test", microvm.App{ID: "app-test", Slug: "test", CreatedAt: at, UpdatedAt: at},
		microvm.AppSessionRuntime{ActiveBuildID: retained, Runtime: microvm.AppRuntimeSpec{Profile: microvm.DefaultSandboxProfileName, InternalPort: 3000}},
		microvm.AppBuild{AppID: "app-test", BuildID: retained, CreatedAt: at}); err != nil {
		t.Fatal(err)
	}
	handler, err := NewPreparedBuildIngressHandler(microvm.NewService(store), "https://r2.example.test", t.TempDir(), filepath.Join(t.TempDir(), "catalog.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		return recorder
	}
	if got := get("/v1/internal/users/usr_test/apps/app-test/builds/" + retained); got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"version":1`) {
		t.Fatalf("retained Build = %d %s", got.Code, got.Body.String())
	}
	missing := get("/v1/internal/users/usr_test/apps/app-test/builds/sha256:" + strings.Repeat("b", 64))
	if missing.Code != http.StatusNotFound || !strings.Contains(missing.Body.String(), `"build_not_found"`) {
		t.Fatalf("missing Build = %d %s", missing.Code, missing.Body.String())
	}
	if got := get("/v1/internal/users/usr_other/apps/app-test/builds/" + retained); got.Code != http.StatusNotFound || strings.Contains(got.Body.String(), "build_not_found") {
		t.Fatalf("cross-tenant Build lookup = %d %s", got.Code, got.Body.String())
	}
	if got := get("/v1/internal/users/usr_test/apps/app-test/builds/not-a-build"); got.Code != http.StatusBadRequest {
		t.Fatalf("invalid Build ID = %d", got.Code)
	}
}
