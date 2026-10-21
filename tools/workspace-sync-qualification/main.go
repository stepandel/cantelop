// workspace-sync-qualification verifies the optimistic file-transfer contract
// on an existing JuiceFS data mount through a temporary loopback NFS export.
// It never opens customer Workspace IDs or changes existing exports.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/host/nfs"
	"github.com/stepandel/cantelop/fire-fuse/workspacesync"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func command(args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	data, err := exec.CommandContext(ctx, args[0], args[1:]...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", args[0], err, bytes.TrimSpace(data))
	}
	return nil
}
func version(data string) string {
	sum := sha256.Sum256(append([]byte{0}, []byte(data)...))
	return hex.EncodeToString(sum[:])
}
func write(name, data string) error {
	f, err := os.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	_, err = f.WriteString(data)
	if err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}

type beforeRead struct {
	io.Reader
	once   sync.Once
	before func()
}

func (r *beforeRead) Read(p []byte) (int, error) { r.once.Do(r.before); return r.Reader.Read(p) }

func run() (failure error) {
	if len(os.Args) != 2 {
		return errors.New("usage: workspace-sync-qualification /absolute/juicefs/data-mount")
	}
	root := os.Args[1]
	if err := nfs.CheckWorkspaceMount(root); err != nil {
		return err
	}
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return err
	}
	id := "wsp_" + hex.EncodeToString(idBytes)
	workspace := filepath.Join(root, id)
	if err := os.Mkdir(workspace, 0700); err != nil {
		return err
	}
	defer func() { failure = errors.Join(failure, os.RemoveAll(workspace)) }()
	if err := os.Chown(workspace, 1000, 1000); err != nil {
		return err
	}
	mount, err := os.MkdirTemp("", "workspace-sync-nfs-")
	if err != nil {
		return err
	}
	defer os.Remove(mount)
	sum := sha256.Sum256([]byte(id))
	fsid := fmt.Sprintf("%x-%x-%x-%x-%x", sum[:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
	target := "127.0.0.1:" + workspace
	if err := command("/usr/sbin/exportfs", "-i", "-o", "rw,async,subtree_check,all_squash,anonuid=1000,anongid=1000,fsid="+fsid, target); err != nil {
		return err
	}
	defer func() { failure = errors.Join(failure, command("/usr/sbin/exportfs", "-u", target)) }()
	if err := command("/usr/sbin/mount.nfs4", "-o", "vers=4.2,proto=tcp", target, mount); err != nil {
		return err
	}
	defer func() { failure = errors.Join(failure, command("/usr/bin/umount", mount)) }()
	store, err := workspacesync.New(root, true)
	if err != nil {
		return err
	}
	store.CheckRoot = func() error { return nfs.CheckWorkspaceMount(root) }
	request := func(ctx context.Context, method, op, name, base string, body io.Reader) *httptest.ResponseRecorder {
		q := url.Values{"op": {op}, "root": {"project"}}
		if name != "" {
			q.Set("path", name)
			q.Set("base", base)
		}
		r := httptest.NewRequest(method, "http://qualification/?"+q.Encode(), body).WithContext(ctx)
		w := httptest.NewRecorder()
		store.Serve(w, r, id)
		return w
	}
	ctx := context.Background()
	stage := "initial scan"
	expect := func(w *httptest.ResponseRecorder, want int) error {
		if w.Code != want {
			return fmt.Errorf("%s: expected %d, received %d: %s", stage, want, w.Code, w.Body)
		}
		return nil
	}
	if err := expect(request(ctx, "GET", "scan", "", "", nil), 200); err != nil {
		return err
	}
	guest := filepath.Join(mount, "project")
	stage = "initial upload"
	if err := expect(request(ctx, "PUT", "write", "binary", "missing", strings.NewReader("\x00\xffsync")), 200); err != nil {
		return err
	}
	if data, err := os.ReadFile(filepath.Join(guest, "binary")); err != nil || string(data) != "\x00\xffsync" {
		return fmt.Errorf("NFS download mismatch: %v", err)
	}
	stage = "read after NFS writer fsync"
	if err := write(filepath.Join(guest, "binary"), "NFS edit"); err != nil {
		return err
	}
	w := request(ctx, "GET", "read", "binary", version("NFS edit"), nil)
	if w.Code != 200 {
		data, readErr := os.ReadFile(filepath.Join(workspace, "project", "binary"))
		info, statErr := os.Stat(filepath.Join(workspace, "project", "binary"))
		fmt.Printf("visibility diagnostic: bytes=%q read=%v stat=%v info=%+v\n", data, readErr, statErr, info)
		for attempt := 0; attempt < 50 && w.Code != 200; attempt++ {
			time.Sleep(100 * time.Millisecond)
			w = request(ctx, "GET", "read", "binary", version("NFS edit"), nil)
		}
		fmt.Printf("visibility diagnostic: settled_status=%d\n", w.Code)
		return errors.New("sync adapter rejected a regular file after NFS fsync")
	}
	if err := expect(request(ctx, "PUT", "write", "binary", version("\x00\xffsync"), strings.NewReader("stale")), 409); err != nil {
		return err
	}
	fmt.Println("PASS: transfer both ways through JuiceFS/NFS and stale-version preservation")
	// Two sync clients share a base; exactly one can publish that version.
	var responses [2]int
	var wait sync.WaitGroup
	for i := range responses {
		wait.Add(1)
		go func(i int) {
			defer wait.Done()
			responses[i] = request(ctx, "PUT", "write", "binary", version("NFS edit"), strings.NewReader(fmt.Sprint("client", i))).Code
		}(i)
	}
	wait.Wait()
	if !(responses[0] == 200 && responses[1] == 409 || responses[1] == 200 && responses[0] == 409) {
		return fmt.Errorf("concurrent clients: %v", responses)
	}
	fmt.Println("PASS: competing sync clients preserve the version precondition")
	// An ordinary NFS writer changes or replaces a file while upload is read.
	for _, kind := range []string{"write", "rename", "delete", "directory", "symlink"} {
		name := "race-" + kind
		file := filepath.Join(guest, name)
		if err := write(file, "base"); err != nil {
			return err
		}
		var changeErr error
		body := &beforeRead{Reader: strings.NewReader("upload"), before: func() {
			switch kind {
			case "write":
				changeErr = write(file, "NFS wins")
			case "rename":
				tmp := file + ".new"
				changeErr = write(tmp, "NFS wins")
				if changeErr == nil {
					changeErr = os.Rename(tmp, file)
				}
			case "delete":
				changeErr = os.Remove(file)
			case "directory":
				changeErr = os.Remove(file)
				if changeErr == nil {
					changeErr = os.Mkdir(file, 0700)
				}
			case "symlink":
				changeErr = os.Remove(file)
				if changeErr == nil {
					changeErr = os.Symlink("../account-fixture", file)
				}
			}
		}}
		w := request(ctx, "PUT", "write", name, version("base"), body)
		if changeErr != nil {
			return changeErr
		}
		if err := expect(w, 409); err != nil {
			return fmt.Errorf("NFS %s: %w", kind, err)
		}
	}
	fmt.Println("PASS: NFS write/rename/delete/type changes during upload are refused")
	cancelled, cancel := context.WithCancel(ctx)
	body := &beforeRead{Reader: strings.NewReader("cancelled"), before: cancel}
	if err := expect(request(cancelled, "PUT", "write", "interrupted", "missing", body), 408); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(guest, "interrupted")); !os.IsNotExist(err) {
		return errors.New("interrupted upload was published")
	}
	if err := write(filepath.Join(mount, "account-fixture"), "untouched"); err != nil {
		return err
	}
	if err := os.Symlink("../account-fixture", filepath.Join(guest, "escape")); err != nil {
		return err
	}
	if w := request(ctx, "GET", "read", "escape", version("untouched"), nil); w.Code < 400 {
		return errors.New("read crossed selected root")
	}
	if data, err := os.ReadFile(filepath.Join(mount, "account-fixture")); err != nil || string(data) != "untouched" {
		return errors.New("sibling state changed")
	}
	if entries, err := os.ReadDir(filepath.Join(workspace, "project")); err != nil {
		return err
	} else {
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".cantelop-sync-") {
				return errors.New("temporary upload leaked")
			}
		}
	}
	fmt.Println("PASS: interruption cleanup and selected-root/sibling confinement")
	fmt.Println("NOTE: version checks remain optimistic against arbitrary POSIX/NFS writers; this is not filesystem CAS")
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"qualified_at": time.Now().UTC(), "contract": "optimistic workspace file synchronization", "workspace_fixture": id})
}
