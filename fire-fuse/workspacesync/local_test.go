package workspacesync

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalLockAndState(t *testing.T) {
	dir := t.TempDir()
	stateDir := t.TempDir()
	local, err := NewLocal(dir, testWorkspace, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if other, err := NewLocal(dir, testWorkspace, stateDir); err == nil {
		other.Close()
		t.Fatal("concurrent owner acquired lock")
	}
	if other, err := NewLocal(dir, testWorkspace, t.TempDir()); err == nil {
		other.Close()
		t.Fatal("alternate metadata directory bypassed ownership")
	}
	w := httptest.NewRecorder()
	local.ServeHTTP(w, httptest.NewRequest("PUT", "http://local/state", strings.NewReader(`{"base":{}}`)))
	if w.Code != 204 {
		t.Fatal(w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	local.ServeHTTP(w, httptest.NewRequest("GET", "http://local/state", nil))
	if w.Code != 200 || w.Body.String() != `{"base":{}}` {
		t.Fatal(w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	local.ServeHTTP(w, httptest.NewRequest("GET", "http://local/state?path=../secret", nil))
	if w.Code != 404 {
		t.Fatal(w.Code)
	}
	if err = local.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := NewLocal(dir, testWorkspace, stateDir)
	if err != nil {
		t.Fatal("OS lock was not released", err)
	}
	defer next.Close()
	w = httptest.NewRecorder()
	next.ServeHTTP(w, httptest.NewRequest("GET", "http://local/state", nil))
	if w.Body.String() != `{"base":{}}` {
		t.Fatal("state did not survive restart")
	}
}

func TestLocalStateMustRemainOutsideProject(t *testing.T) {
	dir := t.TempDir()
	if local, err := NewLocal(dir, testWorkspace, filepath.Join(dir, "state")); err == nil {
		local.Close()
		t.Fatal("state entered project tree")
	}
	local, err := NewLocal(dir, testWorkspace, t.TempDir())
	if err != nil {
		t.Fatal("rejected setup left a lock", err)
	}
	local.Close()
}
