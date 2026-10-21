package workspacesync

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func rootCall(s *Store, method, root, op, name, base string, body []byte) *httptest.ResponseRecorder {
	q := url.Values{"op": {op}, "root": {root}}
	if name != "" {
		q.Set("path", name)
		q.Set("base", base)
	}
	w := httptest.NewRecorder()
	s.Serve(w, httptest.NewRequest(method, "http://private/sync?"+q.Encode(), bytes.NewReader(body)), testWorkspace)
	return w
}

func TestWorkspaceRelativeRoots(t *testing.T) {
	s, dir := fixture(t)
	// Legacy clients still select project/; explicit roots need no binding or metadata.
	if w := call(s, "PUT", "write", "legacy", "missing", []byte("legacy")); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	for _, root := range []string{"", "src/My Project", ".claude"} {
		t.Run(root, func(t *testing.T) {
			w := rootCall(s, "PUT", root, "write", "sample", "missing", []byte(root))
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body)
			}
			if data, err := os.ReadFile(filepath.Join(dir, testWorkspace, root, "sample")); err != nil || string(data) != root {
				t.Fatal("wrong destination", err)
			}
			w = rootCall(s, "GET", root, "scan", "", "", nil)
			var m struct {
				Root    string
				Entries []Entry
			}
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &m) != nil || m.Root != root {
				t.Fatal(w.Code, w.Body)
			}
			if root != "" && (len(m.Entries) != 1 || m.Entries[0].Path != "sample") {
				t.Fatal("included sibling files", m.Entries)
			}
			w = rootCall(s, "GET", root, "read", "sample", version([]byte(root), false), nil)
			if w.Code != 200 || w.Body.String() != root {
				t.Fatal(w.Code, w.Body)
			}
			w = rootCall(s, "DELETE", root, "remove", "sample", version([]byte(root), false), nil)
			if w.Code != 204 {
				t.Fatal(w.Code, w.Body)
			}
		})
	}
	w := rootCall(s, "GET", "", "scan", "", "", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"path":"project/legacy"`) {
		t.Fatal("whole workspace omitted existing content", w.Code, w.Body)
	}
}

func TestRelativeRootRejectsTraversalAndAliases(t *testing.T) {
	s, dir := fixture(t)
	for _, root := range []string{"../other", "/absolute", "a//b", "a/..", "a\\b", ".git", ".ENV", "node_modules", "a/.cantelop-sync-x"} {
		if w := rootCall(s, "GET", root, "scan", "", "", nil); w.Code != 400 {
			t.Fatal(root, w.Code, w.Body)
		}
	}
	for _, query := range []string{"op=scan&root=a&root=b", "op=scan&root=a&directory=b"} {
		w := httptest.NewRecorder()
		s.Serve(w, httptest.NewRequest("GET", "http://private/sync?"+query, nil), testWorkspace)
		if w.Code != 400 {
			t.Fatal(query, w.Code)
		}
	}
	workspace := filepath.Join(dir, testWorkspace)
	if err := os.MkdirAll(filepath.Join(workspace, "real"), 0700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	for name, target := range map[string]string{"alias": "real", "escape": outside} {
		if err := os.Symlink(target, filepath.Join(workspace, name)); err != nil {
			t.Fatal(err)
		}
		for _, root := range []string{name, name + "/nested"} {
			if w := rootCall(s, "GET", root, "scan", "", "", nil); w.Code != 409 {
				t.Fatal(root, w.Code, w.Body)
			}
			if w := rootCall(s, "PUT", root, "write", "file", "missing", []byte("bad")); w.Code != 409 {
				t.Fatal(root, w.Code, w.Body)
			}
		}
	}
	for _, target := range []string{filepath.Join(workspace, "real", "nested"), filepath.Join(outside, "nested")} {
		if _, err := os.Stat(target); !os.IsNotExist(err) {
			t.Fatal("created content through symlink", target, err)
		}
	}
}

func TestRelativeRootReadOnlyDoesNotCreateDirectories(t *testing.T) {
	s, dir := fixture(t)
	s.Writes = false
	for _, root := range []string{"", "new/nested"} {
		w := rootCall(s, "GET", root, "scan", "", "", nil)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"writes":false`) {
			t.Fatal(w.Code, w.Body)
		}
		if w := rootCall(s, "PUT", root, "write", "a", "missing", []byte("bad")); w.Code != 503 {
			t.Fatal(w.Code, w.Body)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, testWorkspace)); !os.IsNotExist(err) {
		t.Fatal("read-only scan created workspace", err)
	}
}

func TestRelativeRootScanPreparesEmptyNestedDirectory(t *testing.T) {
	s, dir := fixture(t)
	if w := rootCall(s, "GET", "missing/nested", "scan", "", "", nil); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if info, err := os.Stat(filepath.Join(dir, testWorkspace, "missing", "nested")); err != nil || !info.IsDir() {
		t.Fatal("scan reported ready without preparing the complete root", err)
	}
	if w := rootCall(s, "GET", "leaf", "scan", "", "", nil); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if info, err := os.Stat(filepath.Join(dir, testWorkspace, "leaf")); err != nil || !info.IsDir() {
		t.Fatal("scan did not prepare the selected directory", err)
	}
}

func TestLocalHelperRejectsRoot(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	local, err := NewLocal(t.TempDir(), testWorkspace, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	for _, query := range []string{"op=scan&root=", "op=scan&root=project", "op=scan&root=src/app"} {
		w := httptest.NewRecorder()
		local.ServeHTTP(w, httptest.NewRequest("GET", "http://local/files?"+query, nil))
		if w.Code != 400 {
			t.Fatal(query, w.Code, w.Body)
		}
	}
}
