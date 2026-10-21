package sessionimage

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionBaseMetadataRejectsUnsupportedRuntimeConfiguration(t *testing.T) {
	for _, test := range []struct {
		name   string
		config map[string]any
		want   string
	}{
		{"dependency-only", map[string]any{}, ""},
		{"inherited-user-and-cwd", map[string]any{"User": "root", "WorkingDir": "/app", "Healthcheck": map[string]any{"Test": []string{"CMD", "false"}}}, ""},
		{"volume", map[string]any{"Volumes": map[string]any{"/data": map[string]any{}}}, "VOLUME"},
		{"trigger", map[string]any{"OnBuild": []string{"COPY . /app"}}, "ONBUILD"},
		{"reserved-env", map[string]any{"Env": []string{"CANTELOP_INTERNAL_PORT=80"}}, "reserved environment"},
	} {
		t.Run(test.name, func(t *testing.T) {
			document, _ := json.Marshal([]any{map[string]any{"Architecture": "amd64", "OS": "linux", "Config": test.config}})
			err := validateSessionBaseMetadata(document)
			if test.want == "" && err != nil || test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("validation = %v, want %q", err, test.want)
			}
		})
	}
}

func TestSessionBaseFilesystemReservesRuntimeAndWorkspace(t *testing.T) {
	for _, test := range []struct {
		name   string
		header tar.Header
		want   string
	}{
		{"asset", tar.Header{Name: "opt/models/model.bin", Typeflag: tar.TypeReg}, ""},
		{"runtime", tar.Header{Name: "__cantelop", Typeflag: tar.TypeDir}, "reserved path"},
		{"bundle", tar.Header{Name: "opt/cantelop/session-runtime.mjs", Typeflag: tar.TypeReg}, "reserved path"},
		{"workspace-asset", tar.Header{Name: "workspace/model.bin", Typeflag: tar.TypeReg}, "mount would hide"},
		{"workspace-link", tar.Header{Name: "workspace", Typeflag: tar.TypeSymlink, Linkname: "/app"}, "must be a directory"},
		{"account-link", tar.Header{Name: "etc/passwd", Typeflag: tar.TypeSymlink, Linkname: "/other"}, "regular account file"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var document bytes.Buffer
			writer := tar.NewWriter(&document)
			for _, header := range []*tar.Header{{Name: "usr/lib/x86_64-linux-gnu/ld-linux-x86-64.so.2", Typeflag: tar.TypeReg}, &test.header} {
				if err := writer.WriteHeader(header); err != nil {
					t.Fatal(err)
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			_, err := inspectSessionBaseArchive(&document)
			if test.want == "" && err != nil || test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("inspection = %v, want %q", err, test.want)
			}
		})
	}
}

func TestManagedAccountsPreserveDistributionAccountsAndReplaceReservedIdentity(t *testing.T) {
	base := "root:x:0:0:root:/root:/bin/sh\napp:x:10001:10001:app:/app:/bin/sh\ncantelop:x:2000:2000:old:/old:/bin/sh\nservice:x:3000:3000:service:/srv:/bin/sh\n"
	directory := t.TempDir()
	if err := writeSessionAssets(directory, sessionBaseFiles{passwd: base, group: "root:x:0:\n"}); err != nil {
		t.Fatal(err)
	}
	passwd, err := os.ReadFile(filepath.Join(directory, sessionAssetsDirectory, "passwd"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(passwd), "root:x:0:") || !strings.Contains(string(passwd), "service:x:3000:") || strings.Contains(string(passwd), "app:x:10001:") || strings.Contains(string(passwd), "/old") || strings.Count(string(passwd), "cantelop:x:") != 1 {
		t.Fatalf("managed passwd = %s", passwd)
	}
}
