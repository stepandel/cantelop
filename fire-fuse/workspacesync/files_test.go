package workspacesync

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

const testWorkspace = "wsp_11111111111111111111111111111111"

func fixture(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, e := New(dir, true)
	if e != nil {
		t.Fatal(e)
	}
	s.WorkspaceMode = 0700
	return s, dir
}

func TestUnavailableBackingMountFailsClosed(t *testing.T) {
	s, dir := fixture(t)
	s.CheckRoot = func() error { return errors.New("mount unavailable") }
	if w := call(s, "GET", "scan", "", "", nil); w.Code != 503 {
		t.Fatal(w.Code, w.Body)
	}
	if w := call(s, "PUT", "write", "a", "missing", []byte("x")); w.Code != 503 {
		t.Fatal(w.Code, w.Body)
	}
	if _, err := os.Stat(filepath.Join(dir, testWorkspace)); !os.IsNotExist(err) {
		t.Fatal("mount failure created fallback storage")
	}
}

func TestSpecialFilesAreNotRead(t *testing.T) {
	s, dir := fixture(t)
	call(s, "GET", "scan", "", "", nil)
	if err := syscall.Mkfifo(filepath.Join(dir, testWorkspace, "project", "pipe"), 0600); err != nil {
		t.Fatal(err)
	}
	if w := call(s, "GET", "read", "pipe", version([]byte("x"), false), nil); w.Code != 409 {
		t.Fatal(w.Code)
	}
	if w := call(s, "GET", "scan", "", "", nil); w.Code != 200 || strings.Contains(w.Body.String(), "pipe") {
		t.Fatal(w.Code, w.Body)
	}
}

func TestContentUpdatesPreserveDestinationPrivacy(t *testing.T) {
	s, dir := fixture(t)
	if w := call(s, "PUT", "write", "private", "missing", []byte("old")); w.Code != 200 {
		t.Fatal(w.Code)
	}
	p := filepath.Join(dir, testWorkspace, "project", "private")
	if err := os.Chmod(p, 0600); err != nil {
		t.Fatal(err)
	}
	if w := call(s, "PUT", "write", "private", version([]byte("old"), false), []byte("new")); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	info, err := os.Stat(p)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("content update broadened file permissions")
	}
}
func call(s *Store, method, op, p, base string, data []byte) *httptest.ResponseRecorder {
	q := url.Values{"op": {op}}
	if p != "" {
		q.Set("path", p)
		q.Set("base", base)
	}
	r := httptest.NewRequest(method, "http://private/sync?"+q.Encode(), bytes.NewReader(data))
	w := httptest.NewRecorder()
	s.Serve(w, r, testWorkspace)
	return w
}
func TestTransferWithoutSandbox(t *testing.T) {
	s, dir := fixture(t)
	w := call(s, "GET", "scan", "", "", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"entries":[]`) {
		t.Fatal(w.Code, w.Body)
	}
	if info, err := os.Stat(filepath.Join(dir, testWorkspace, "project")); err != nil || !info.IsDir() {
		t.Fatal("empty sync did not prepare agent cwd")
	}
	data := []byte{0, 255, 1, 0, 42}
	w = call(s, "PUT", "write", "src/file.bin", "missing", data)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var entry Entry
	if e := json.Unmarshal(w.Body.Bytes(), &entry); e != nil {
		t.Fatal(e)
	}
	w = call(s, "GET", "read", entry.Path, entry.Version, nil)
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), data) {
		t.Fatal(w.Code, w.Body)
	}
	w = call(s, "PUT", "write", entry.Path, "missing", []byte("stale"))
	if w.Code != 409 {
		t.Fatal(w.Code, w.Body)
	}
	w = call(s, "DELETE", "remove", entry.Path, version([]byte("wrong"), false), nil)
	if w.Code != 409 {
		t.Fatal(w.Code)
	}
	// Simulates a normal filesystem writer, including a sandbox writing via NFS.
	p := filepath.Join(dir, testWorkspace, "project", "src/file.bin")
	if e := os.WriteFile(p, []byte("remote"), 0755); e != nil {
		t.Fatal(e)
	}
	w = call(s, "GET", "scan", "", "", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), version([]byte("remote"), false)) {
		t.Fatal(w.Code, w.Body)
	}
	w = call(s, "GET", "read", entry.Path, entry.Version, nil)
	if w.Code != 409 {
		t.Fatal(w.Code)
	}
	w = call(s, "DELETE", "remove", entry.Path, version([]byte("remote"), false), nil)
	if w.Code != 204 {
		t.Fatal(w.Code, w.Body)
	}
}
func TestConfinementAndUnsupportedEntries(t *testing.T) {
	s, dir := fixture(t)
	for _, p := range []string{"../secret", "/etc/passwd", "a/../../secret", "a//b", "a\\b", ".git/config", ".GIT/marker.txt", ".env", ".ENV", "node_modules/a", "Node_Modules/a", ".cantelop-sync-x", ".CANTELOP-SYNC-x", "a\x00b"} {
		t.Run(url.PathEscape(p), func(t *testing.T) {
			s, _ := fixture(t)
			w := call(s, "PUT", "write", p, "missing", []byte("x"))
			if w.Code != 400 {
				t.Fatal(w.Code, w.Body)
			}
		})
	}
	call(s, "PUT", "write", "ok", "missing", []byte("ok"))
	for _, op := range []string{"read", "remove"} {
		method := map[string]string{"read": "GET", "remove": "DELETE"}[op]
		w := call(s, method, op, ".GIT/marker.txt", "missing", nil)
		if w.Code != 400 {
			t.Fatal(op, w.Code, w.Body)
		}
	}
	project := filepath.Join(dir, testWorkspace, "project")
	outside := t.TempDir()
	if e := os.WriteFile(filepath.Join(outside, "secret"), []byte("secret"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(outside, filepath.Join(project, "escape")); e != nil {
		t.Fatal(e)
	}
	for _, op := range []string{"read", "write", "remove"} {
		method := map[string]string{"read": "GET", "write": "PUT", "remove": "DELETE"}[op]
		w := call(s, method, op, "escape/secret", "missing", []byte("bad"))
		if w.Code < 400 {
			t.Fatal(op, w.Code)
		}
	}
	data, _ := os.ReadFile(filepath.Join(outside, "secret"))
	if string(data) != "secret" {
		t.Fatal("escaped root")
	}
	if w := call(s, "GET", "scan", "", "", nil); w.Code != 200 || strings.Contains(w.Body.String(), "escape") {
		t.Fatal(w.Code, w.Body)
	}
	if e := os.Remove(filepath.Join(project, "escape")); e != nil {
		t.Fatal(e)
	}
	if e := os.Link(filepath.Join(project, "ok"), filepath.Join(project, "hard")); e != nil {
		t.Fatal(e)
	}
	if w := call(s, "GET", "scan", "", "", nil); w.Code != 409 {
		t.Fatal(w.Code, w.Body)
	}
}

func TestScanSkipsReservedCaseAliases(t *testing.T) {
	s, dir := fixture(t)
	if w := call(s, "GET", "scan", "", "", nil); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	project := filepath.Join(dir, testWorkspace, "project")
	if err := os.Mkdir(filepath.Join(project, ".GIT"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".GIT", "marker.txt"), []byte("excluded"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "README.md"), []byte("included"), 0600); err != nil {
		t.Fatal(err)
	}
	w := call(s, "GET", "scan", "", "", nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var manifest struct {
		Entries []Entry `json:"entries"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Entries) != 1 || manifest.Entries[0].Path != "README.md" {
		t.Fatal("scan included a reserved alias or lost an ordinary file", manifest.Entries)
	}
}

func TestWriteRejectsInRootSymlinkAlias(t *testing.T) {
	s, dir := fixture(t)
	if w := call(s, "GET", "scan", "", "", nil); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	project := filepath.Join(dir, testWorkspace, "project")
	if err := os.Mkdir(filepath.Join(project, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".git", filepath.Join(project, "alias")); err != nil {
		t.Fatal(err)
	}
	w := call(s, "PUT", "write", "alias/marker.txt", "missing", []byte("excluded"))
	if w.Code != 409 {
		t.Fatal(w.Code, w.Body)
	}
	if _, err := os.Stat(filepath.Join(project, ".git", "marker.txt")); !os.IsNotExist(err) {
		t.Fatal("write reached reserved directory", err)
	}
}

func TestProjectRootCannotAliasCredentials(t *testing.T) {
	s, dir := fixture(t)
	workspace := filepath.Join(dir, testWorkspace)
	if e := os.MkdirAll(filepath.Join(workspace, ".claude"), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(".claude", filepath.Join(workspace, "project")); e != nil {
		t.Fatal(e)
	}
	if w := call(s, "GET", "scan", "", "", nil); w.Code != 409 {
		t.Fatal(w.Code, w.Body)
	}
}
func TestWriteGateAndLimits(t *testing.T) {
	s, dir := fixture(t)
	s.Writes = false
	if w := call(s, "PUT", "write", "x", "missing", []byte("x")); w.Code != 503 {
		t.Fatal(w.Code)
	}
	if _, e := os.Stat(filepath.Join(dir, testWorkspace)); !os.IsNotExist(e) {
		t.Fatal("disabled writes created storage")
	}
	s.Writes = true
	if w := call(s, "PUT", "write", "x", "missing", make([]byte, MaxFileBytes+1)); w.Code != 413 {
		t.Fatal(w.Code)
	}
	if w := call(s, "GET", "scan", "", "", nil); w.Code != 200 || strings.Contains(w.Body.String(), TempPrefix) {
		t.Fatal(w.Code, w.Body)
	}
}

func TestReadOnlyScanHasNoSideEffectsAndWritesInheritOwnership(t *testing.T) {
	s, dir := fixture(t)
	s.Writes = false
	if w := call(s, "GET", "scan", "", "", nil); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if _, err := os.Stat(filepath.Join(dir, testWorkspace)); !os.IsNotExist(err) {
		t.Fatal("read-only scan created storage")
	}
	s.Writes = true
	s.WorkspaceMode = 0
	if w := call(s, "PUT", "write", "a", "missing", []byte("x")); w.Code != 409 {
		t.Fatal("hosted store created a Workspace directory it does not own", w.Code)
	}
	workspace := filepath.Join(dir, testWorkspace)
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(workspace, os.ModeSetgid|0770); err != nil {
		t.Fatal(err)
	}
	if w := call(s, "PUT", "write", "src/a", "missing", []byte("x")); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	for _, p := range []string{"project", "project/src", "project/src/a"} {
		info, err := os.Stat(filepath.Join(workspace, p))
		if err != nil || info.Mode().Perm()&0020 == 0 {
			t.Fatal("synced entry is not writable by the Workspace group", p)
		}
	}
}

func TestInspectStreamsTheReadVersion(t *testing.T) {
	directory := t.TempDir()
	content := bytes.Repeat([]byte("streamed content\n"), 4096)
	if err := os.WriteFile(filepath.Join(directory, "tool"), content, 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	data, entry, err := read(root, "tool")
	if err != nil || !bytes.Equal(data, content) {
		t.Fatalf("read() = %d bytes, %v", len(data), err)
	}
	inspected, err := inspect(root, "tool")
	if err != nil || inspected != entry || inspected.Version != version(content, true) || !inspected.Executable {
		t.Fatalf("inspect() = %#v, %v; read() = %#v", inspected, err, entry)
	}
}
