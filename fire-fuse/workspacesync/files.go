// Package workspacesync implements the private, bounded transfer protocol behind
// workspace.sync(). It has no authority to select an App or a Workspace.
package workspacesync

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/model"
	"github.com/stepandel/cantelop/fire-fuse/syncprotocol"
)

const (
	MaxFileBytes = syncprotocol.MaxFileBytes
	MaxEntries   = syncprotocol.MaxEntries
	MaxScanBytes = syncprotocol.MaxScanBytes
	TempPrefix   = syncprotocol.TempPrefix
)

var versionPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Entry = syncprotocol.Entry

// Store is configured exclusively by the host. Writes must remain disabled on
// hosted storage until the JuiceFS/NFS concurrent-writer qualification passes.
type Store struct {
	// Hosted stores revalidate the existing data-only mount policy on access.
	CheckRoot func() error
	// WorkspaceMode lets local and test stores create a missing Workspace
	// directory with this mode. Hosted stores leave it zero: the NFS export path
	// owns Workspace directory creation and its workload ownership.
	WorkspaceMode os.FileMode
	localRoot     *os.Root
	Root          string
	Writes        bool
	locks         [64]chan struct{}
	permits       chan struct{}
}

// owner is the identity and sharing mode new entries inherit from the
// Workspace directory, so synced files stay writable by the Sandbox workload.
type owner struct {
	uid, gid int
	shared   bool
	setgid   bool
}

func ownerOf(root *os.Root) (owner, error) {
	info, err := root.Stat(".")
	if err != nil {
		return owner{}, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return owner{}, errors.New("unsupported file ownership")
	}
	return owner{uid: int(st.Uid), gid: int(st.Gid), shared: info.Mode().Perm()&0020 != 0, setgid: info.Mode()&os.ModeSetgid != 0}, nil
}

func (o owner) dirMode() os.FileMode {
	mode := os.FileMode(0755)
	if o.shared {
		mode |= 0020
	}
	if o.setgid {
		mode |= os.ModeSetgid
	}
	return mode
}

func New(root string, writes bool) (*Store, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return nil, errors.New("invalid workspace sync storage configuration")
	}
	store := &Store{Root: root, Writes: writes, permits: make(chan struct{}, 8)}
	for i := range store.locks {
		store.locks[i] = make(chan struct{}, 1)
	}
	return store, nil
}

// ValidPath uses the same portable grammar as the SDK. The project root itself
// and reserved temporary entries are never customer mutation targets.
func ValidPath(p string) bool { return syncprotocol.ValidPath(p) }

func fail(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code}})
}

// Serve is called only after authorization, with the server-resolved Workspace.
// Each call is bounded; no caller-supplied host path or private URL is accepted.
func (s *Store) Serve(w http.ResponseWriter, r *http.Request, workspaceID string) {
	s.ServeAuthorized(w, r, workspaceID, nil)
}

// Drain waits for operations admitted before archival. Authorization is checked
// under the same lock, so queued operations cannot reuse a pre-archive decision.
func (s *Store) Drain(ctx context.Context, workspaceID string) error {
	key := sha256.Sum256([]byte(workspaceID))
	lock := s.locks[int(key[0])%len(s.locks)]
	select {
	case lock <- struct{}{}:
		<-lock
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Store) ServeAuthorized(w http.ResponseWriter, r *http.Request, workspaceID string, authorize func() bool) {
	w.Header().Set("Cache-Control", "no-store")
	if s == nil {
		fail(w, http.StatusServiceUnavailable, "sync_unavailable")
		return
	}
	if model.ValidateWorkspaceID(workspaceID) != nil {
		fail(w, http.StatusBadRequest, "invalid_workspace")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(30 * time.Second))
	defer http.NewResponseController(w).SetReadDeadline(time.Time{})
	// Queue on the Workspace lock before taking a global permit, so a busy
	// Workspace cannot hold permits idle and starve other Workspaces.
	key := sha256.Sum256([]byte(workspaceID))
	lock := s.locks[int(key[0])%len(s.locks)]
	select {
	case lock <- struct{}{}:
		defer func() { <-lock }()
	case <-ctx.Done():
		fail(w, http.StatusServiceUnavailable, "sync_busy")
		return
	}
	select {
	case s.permits <- struct{}{}:
		defer func() { <-s.permits }()
	case <-ctx.Done():
		fail(w, http.StatusServiceUnavailable, "sync_busy")
		return
	}
	if authorize != nil && !authorize() {
		fail(w, http.StatusNotFound, "resource_not_found")
		return
	}
	if s.CheckRoot != nil {
		if err := s.CheckRoot(); err != nil {
			fail(w, http.StatusServiceUnavailable, "sync_storage_unavailable")
			return
		}
	}
	q, queryErr := url.ParseQuery(r.URL.RawQuery)
	if queryErr != nil {
		fail(w, http.StatusBadRequest, "invalid_sync_request")
		return
	}
	op := q.Get("op")
	p := q.Get("path")
	base := q.Get("base")
	for name, values := range q {
		if len(values) != 1 || (name != "op" && name != "path" && name != "base" && name != "executable" && name != "root") {
			fail(w, http.StatusBadRequest, "invalid_sync_request")
			return
		}
	}
	// Explicit root= selects the whole Workspace. Omitting root retains the
	// original project/ wire default for already-published SDK clients.
	rootName := "project"
	if q.Has("root") {
		rootName = q.Get("root")
		if rootName != "" && !ValidPath(rootName) {
			fail(w, http.StatusBadRequest, "invalid_sync_root")
			return
		}
		// The local helper serves exactly its configured directory.
		if s.localRoot != nil {
			fail(w, http.StatusBadRequest, "invalid_sync_root")
			return
		}
	}
	method := ""
	switch op {
	case "scan", "read":
		method = "GET"
	case "write":
		method = "PUT"
	case "remove":
		method = "DELETE"
	}
	if method == "" || r.Method != method {
		fail(w, http.StatusBadRequest, "invalid_sync_request")
		return
	}
	if op == "scan" {
		if q.Has("path") || q.Has("base") || q.Has("executable") {
			fail(w, http.StatusBadRequest, "invalid_sync_request")
			return
		}
	} else if !ValidPath(p) || (base != "missing" && !versionPattern.MatchString(base)) || (q.Has("executable") && (op != "write" || (q.Get("executable") != "0" && q.Get("executable") != "1"))) {
		fail(w, http.StatusBadRequest, "invalid_sync_request")
		return
	}
	if (op == "write" || op == "remove") && !s.Writes {
		fail(w, http.StatusServiceUnavailable, "sync_writes_not_enabled")
		return
	}
	// Initial reconciliation also prepares an empty project for agent cwd use,
	// but only when mutations are enabled: read-only access has no side effects.
	root, ownership, err := s.project(workspaceID, rootName, op == "write" || (op == "scan" && s.Writes))
	if errors.Is(err, os.ErrNotExist) && op == "scan" {
		writeManifest(w, []Entry{}, s.Writes, rootName)
		return
	}
	if err != nil {
		fail(w, http.StatusConflict, "sync_root_unavailable")
		return
	}
	defer root.Close()
	switch op {
	case "scan":
		entries := []Entry{}
		var bytes int64
		visited := 0
		err = s.scan(ctx, root, ".", &entries, &bytes, &visited)
		if err != nil {
			fail(w, http.StatusConflict, "sync_scan_failed")
			return
		}
		writeManifest(w, entries, s.Writes, rootName)
	case "read":
		data, entry, e := read(root, p)
		if e != nil || entry.Version != base {
			fail(w, http.StatusConflict, "sync_conflict")
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.Header().Set("ETag", `"`+entry.Version+`"`)
		_, _ = w.Write(data)
	case "write":
		data, e := io.ReadAll(io.LimitReader(r.Body, MaxFileBytes+1))
		if e != nil || len(data) > MaxFileBytes {
			fail(w, http.StatusRequestEntityTooLarge, "sync_file_too_large")
			return
		}
		if ctx.Err() != nil {
			fail(w, http.StatusRequestTimeout, "sync_timeout")
			return
		}
		token := make([]byte, 16)
		if _, e = rand.Read(token); e != nil {
			fail(w, http.StatusServiceUnavailable, "sync_unavailable")
			return
		}
		temp := TempPrefix + hex.EncodeToString(token)
		file, e := root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			fail(w, http.StatusConflict, "sync_write_failed")
			return
		}
		defer root.Remove(temp)
		mode := os.FileMode(0644)
		executable := q.Get("executable") == "1"
		if executable {
			mode = 0755
		}
		if ownership.shared {
			mode |= 0020
		}
		// Content updates must not broaden an existing destination's privacy.
		if previous, err := root.Lstat(p); err == nil && previous.Mode().IsRegular() {
			mode = previous.Mode().Perm()
			if !executable {
				mode &^= 0111
			} else if mode&0111 == 0 {
				mode |= 0100
			}
		}
		_, e = file.Write(data)
		if e == nil {
			e = file.Chmod(mode)
		}
		if e == nil && os.Geteuid() == 0 {
			e = file.Chown(ownership.uid, ownership.gid)
		}
		if e == nil {
			e = file.Sync()
		}
		e = errors.Join(e, file.Close())
		if e != nil {
			fail(w, http.StatusConflict, "sync_write_failed")
			return
		}
		if !matches(root, p, base) {
			fail(w, http.StatusConflict, "sync_conflict")
			return
		}
		if e = mkdirAll(root, path.Dir(p), ownership); e != nil {
			fail(w, http.StatusConflict, "sync_write_failed")
			return
		}
		// Preconditions are optimistic against arbitrary NFS writers. Hosted writes
		// are rollout-gated; this is deliberately not advertised as filesystem CAS.
		if !matches(root, p, base) {
			fail(w, http.StatusConflict, "sync_conflict")
			return
		}
		if ctx.Err() != nil {
			fail(w, http.StatusRequestTimeout, "sync_timeout")
			return
		}
		if base == "missing" {
			e = publishNew(root, temp, p)
		} else {
			e = root.Rename(temp, p)
		}
		if e != nil {
			if errors.Is(e, os.ErrExist) {
				fail(w, http.StatusConflict, "sync_conflict")
				return
			}
			fail(w, http.StatusConflict, "sync_write_failed")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Entry{Path: p, Version: version(data, executable), Size: int64(len(data)), Executable: executable})
	case "remove":
		if base == "missing" || !matches(root, p, base) {
			fail(w, http.StatusConflict, "sync_conflict")
			return
		}
		if e := root.Remove(p); e != nil {
			fail(w, http.StatusConflict, "sync_conflict")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// writeManifest sends an explicit length so proxies can reject an oversized
// manifest before committing to a status instead of truncating it.
func writeManifest(w http.ResponseWriter, entries []Entry, writes bool, rootName string) {
	data, err := json.Marshal(map[string]any{"protocolVersion": 1, "root": rootName, "writes": writes, "entries": entries})
	if err != nil || len(data) > syncprotocol.MaxManifestBytes {
		fail(w, http.StatusConflict, "sync_scan_failed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

func version(data []byte, executable bool) string {
	h := newVersionHash(executable)
	_, _ = h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

// newVersionHash starts a content version: SHA-256 over an executable flag
// byte followed by the file content.
func newVersionHash(executable bool) hash.Hash {
	h := sha256.New()
	if executable {
		_, _ = h.Write([]byte{1})
	} else {
		_, _ = h.Write([]byte{0})
	}
	return h
}

// read returns a regular file's content and its manifest entry.
func read(root *os.Root, p string) ([]byte, Entry, error) {
	return load(root, p, true)
}

// inspect returns a regular file's manifest entry. Content is hashed as it is
// read rather than buffered, so scans and preconditions do not hold whole files.
func inspect(root *os.Root, p string) (Entry, error) {
	_, entry, err := load(root, p, false)
	return entry, err
}

func load(root *os.Root, p string, retain bool) ([]byte, Entry, error) {
	f, e := openContent(root, p)
	if e != nil {
		return nil, Entry{}, e
	}
	defer f.Close()
	before, e := f.Stat()
	if e != nil || !before.Mode().IsRegular() || before.Size() > MaxFileBytes {
		return nil, Entry{}, errors.New("unsupported file")
	}
	if st, ok := before.Sys().(*syscall.Stat_t); ok && st.Nlink > 1 {
		return nil, Entry{}, errors.New("hard links unsupported")
	}
	executable := before.Mode()&0111 != 0
	h := newVersionHash(executable)
	var content *bytes.Buffer
	var destination io.Writer = h
	if retain {
		content = bytes.NewBuffer(make([]byte, 0, before.Size()))
		destination = io.MultiWriter(h, content)
	}
	size, e := io.Copy(destination, io.LimitReader(f, MaxFileBytes+1))
	if e != nil || size > MaxFileBytes {
		return nil, Entry{}, errors.New("file limit")
	}
	after, e := f.Stat()
	if e != nil || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) ||
		(after.Mode()&0111 != 0) != executable {
		return nil, Entry{}, errors.New("file changed")
	}
	var data []byte
	if retain {
		data = content.Bytes()
	}
	return data, Entry{Path: p, Version: hex.EncodeToString(h.Sum(nil)), Size: size, Executable: executable}, nil
}

func matches(root *os.Root, p, base string) bool {
	_, e := root.Lstat(p)
	if errors.Is(e, os.ErrNotExist) {
		return base == "missing"
	}
	if e != nil || base == "missing" {
		return false
	}
	entry, e := inspect(root, p)
	return e == nil && entry.Version == base
}

func (s *Store) scan(ctx context.Context, root *os.Root, dir string, entries *[]Entry, bytes *int64, visited *int) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	f, e := root.Open(dir)
	if e != nil {
		return e
	}
	defer f.Close()
	for {
		children, batchErr := f.ReadDir(128)
		if batchErr != nil && !errors.Is(batchErr, io.EOF) {
			return batchErr
		}
		for _, child := range children {
			*visited++
			if *visited > MaxEntries || ctx.Err() != nil {
				return errors.New("scan limit")
			}
			p := path.Join(dir, child.Name())
			if !ValidPath(p) {
				continue
			}
			// Symlinks and special files are not synchronized in version 1.
			// Direct read/write/remove of them still fails closed.
			if child.Type()&os.ModeSymlink != 0 || (!child.IsDir() && !child.Type().IsRegular()) {
				continue
			}
			if child.IsDir() {
				if e = s.scan(ctx, root, p, entries, bytes, visited); e != nil {
					return e
				}
				continue
			}
			if len(*entries) >= MaxEntries {
				return errors.New("entry limit")
			}
			entry, e := inspect(root, p)
			if e != nil {
				return e
			}
			*bytes += entry.Size
			if *bytes > MaxScanBytes {
				return errors.New("scan limit")
			}
			*entries = append(*entries, entry)
		}
		if errors.Is(batchErr, io.EOF) {
			break
		}
	}
	return nil
}

// openDirectory rejects symlinks at a selected directory boundary. The descriptor stays
// pinned while os.Root is constructed, including across concurrent renames.
func openDirectory(parent *os.Root, name string) (*os.Root, error) {
	info, err := parent.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("unsafe root")
	}
	f, e := parent.OpenFile(name, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	prefix := "/proc/self/fd/"
	if runtime.GOOS == "darwin" {
		prefix = "/dev/fd/"
	}
	return os.OpenRoot(filepath.Join(prefix, strconv.FormatUint(uint64(f.Fd()), 10)))
}

// project opens the synchronized directory and the ownership new entries must
// inherit from the directory that contains it.
func (s *Store) project(id, rootName string, create bool) (*os.Root, owner, error) {
	if s.localRoot != nil {
		root, e := openDirectory(s.localRoot, ".")
		if e != nil {
			return nil, owner{}, e
		}
		ownership, e := ownerOf(root)
		if e != nil {
			root.Close()
			return nil, owner{}, e
		}
		return root, ownership, nil
	}
	base, e := os.OpenRoot(s.Root)
	if e != nil {
		return nil, owner{}, e
	}
	defer base.Close()
	if create && s.WorkspaceMode != 0 {
		e = base.Mkdir(id, 0700)
		if e == nil {
			e = base.Chmod(id, s.WorkspaceMode)
		}
		if e != nil && !errors.Is(e, os.ErrExist) {
			return nil, owner{}, e
		}
	}
	workspace, e := openDirectory(base, id)
	if e != nil {
		return nil, owner{}, e
	}
	defer workspace.Close()
	ownership, e := ownerOf(workspace)
	if e != nil {
		return nil, owner{}, e
	}
	root, e := openDirectory(workspace, ".")
	if e != nil {
		return nil, owner{}, e
	}
	if rootName != "" {
		// Pin every component; a relative root cannot follow a symlink into a
		// sibling directory, another Workspace, or host files.
		parts := strings.Split(rootName, "/")
		for _, part := range parts {
			created := false
			if create {
				err := root.Mkdir(part, 0755)
				if err != nil && !errors.Is(err, os.ErrExist) {
					root.Close()
					return nil, owner{}, err
				}
				created = err == nil
			}
			next, err := openDirectory(root, part)
			if err == nil && created {
				err = next.Chmod(".", ownership.dirMode())
				if err == nil && os.Geteuid() == 0 {
					err = next.Chown(".", ownership.uid, ownership.gid)
				}
				if err != nil {
					next.Close()
				}
			}
			if err != nil {
				// Do not leave a directory without the inherited ownership behind.
				if created {
					_ = root.Remove(part)
				}
				root.Close()
				return nil, owner{}, err
			}
			root.Close()
			root = next
		}
	}
	return root, ownership, nil
}

func mkdirAll(root *os.Root, p string, ownership owner) error {
	if p == "." {
		return nil
	}
	parts := strings.Split(p, "/")
	at := ""
	for _, part := range parts {
		at = path.Join(at, part)
		e := root.Mkdir(at, 0755)
		if e != nil && !errors.Is(e, os.ErrExist) {
			return e
		}
		info, err := root.Lstat(at)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("unsafe directory")
		}
		if e == nil {
			if err = root.Chmod(at, ownership.dirMode()); err != nil {
				return err
			}
		}
		if e == nil && os.Geteuid() == 0 {
			if err = root.Chown(at, ownership.uid, ownership.gid); err != nil {
				return err
			}
		}
	}
	return nil
}
