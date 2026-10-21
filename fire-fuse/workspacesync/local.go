package workspacesync

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/stepandel/cantelop/fire-fuse/runtime/model"
	"golang.org/x/sys/unix"
)

// Local is the CLI's parent-scoped filesystem adapter. The SDK never performs
// unsafe string-based traversal of untrusted local directory entries.
type Local struct {
	root        *os.Root
	state       *os.Root
	lock        *os.File
	files       *Store
	directory   string
	workspaceID string
}

func NewLocal(directory, workspaceID, stateDirectory string) (*Local, error) {
	if model.ValidateWorkspaceID(workspaceID) != nil || !filepath.IsAbs(stateDirectory) {
		return nil, errors.New("invalid sync configuration")
	}
	directory, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return nil, err
	}
	directory, err = filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	lock, err := root.OpenFile(".", os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		root.Close()
		return nil, err
	}
	cleanup := func(err error) (*Local, error) { lock.Close(); root.Close(); return nil, err }
	// Lock the directory inode itself: Git clean and alternate metadata locations
	// cannot unlink a lock file and let a second owner into the same directory.
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return cleanup(errors.New("sync_locked"))
	}
	identity := sha256.Sum256([]byte(directory))
	statePath := filepath.Join(stateDirectory, hex.EncodeToString(identity[:]))
	if inside(directory, statePath) {
		return cleanup(errors.New("sync state must be outside the synchronized directory"))
	}
	if err = os.MkdirAll(statePath, 0700); err != nil {
		return cleanup(err)
	}
	statePath, err = filepath.EvalSymlinks(statePath)
	if err != nil {
		return cleanup(err)
	}
	if inside(directory, statePath) {
		return cleanup(errors.New("sync state must be outside the synchronized directory"))
	}
	state, err := os.OpenRoot(statePath)
	if err != nil {
		return cleanup(err)
	}
	info, err := state.Stat(".")
	if err != nil || info.Mode().Perm()&0077 != 0 {
		state.Close()
		return cleanup(errors.New("sync state directory is not private"))
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) != os.Geteuid() {
		state.Close()
		return cleanup(errors.New("sync state directory has an invalid owner"))
	}
	files, err := New(directory, true)
	if err != nil {
		state.Close()
		return cleanup(err)
	}
	files.localRoot = root
	return &Local{root: root, state: state, lock: lock, files: files, directory: directory, workspaceID: workspaceID}, nil
}

func inside(directory, target string) bool {
	relative, err := filepath.Rel(directory, target)
	return err != nil || relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

// Directory is the resolved absolute directory being synchronized.
func (l *Local) Directory() string { return l.directory }

func (l *Local) Close() error { return errors.Join(l.lock.Close(), l.root.Close(), l.state.Close()) }

func (l *Local) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path == "/files" {
		l.files.Serve(w, r, l.workspaceID)
		return
	}
	if r.URL.Path != "/state" || r.URL.RawQuery != "" {
		fail(w, 404, "not_found")
		return
	}
	name := l.workspaceID + ".json"
	switch r.Method {
	case "GET":
		file, err := l.state.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if errors.Is(err, os.ErrNotExist) {
			w.WriteHeader(204)
			return
		}
		if err != nil {
			fail(w, 409, "sync_invalid_state")
			return
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() > 4<<20 {
			fail(w, 409, "sync_invalid_state")
			return
		}
		if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Nlink != 1 {
			fail(w, 409, "sync_invalid_state")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.Copy(w, io.LimitReader(file, 4<<20))
	case "PUT":
		data, err := io.ReadAll(io.LimitReader(r.Body, (4<<20)+1))
		if err != nil || len(data) > 4<<20 {
			fail(w, 413, "sync_invalid_state")
			return
		}
		var token [16]byte
		if _, err = rand.Read(token[:]); err != nil {
			fail(w, 503, "sync_unavailable")
			return
		}
		temp := TempPrefix + hex.EncodeToString(token[:])
		defer l.state.Remove(temp)
		file, err := l.state.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			fail(w, 409, "sync_invalid_state")
			return
		}
		_, err = file.Write(data)
		if err == nil {
			err = file.Sync()
		}
		err = errors.Join(err, file.Close())
		if err == nil {
			err = l.state.Rename(temp, name)
		}
		if err != nil {
			fail(w, 409, "sync_invalid_state")
			return
		}
		w.WriteHeader(204)
	default:
		fail(w, 405, "invalid_method")
	}
}
