package nfs

import (
	"context"
	"errors"
	"github.com/stepandel/cantelop/fire-fuse/runtime/model"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

// DirectoryQuota installs a JuiceFS hard quota before native workloads or sync
// can write. Limits are rounded DOWN to GiB. The quota covers Workspace files
// only; Turso database storage is not capped by Fleet allocations.
func DirectoryQuota(binary, metadata, root, prefix string) func(string, int64) error {
	return func(workspace string, bytes int64) error {
		if model.ValidateWorkspaceID(workspace) != nil || metadata == "" || !filepath.IsAbs(binary) || !filepath.IsAbs(root) || bytes < (1<<30) {
			return errors.New("managed storage quota unavailable")
		}
		path := filepath.Join(root, workspace)
		if err := os.Mkdir(path, 0700); err != nil && !os.IsExist(err) {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("unsafe managed Workspace")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		// Command diagnostics can contain metadata credentials: never return output.
		cmd := exec.CommandContext(ctx, binary, "quota", "set", metadata, "--path", filepath.Join("/", prefix, workspace), "--capacity", strconv.FormatInt(bytes>>30, 10), "--inodes", "200000")
		if err = cmd.Run(); err != nil {
			return errors.New("managed storage quota unavailable")
		}
		return nil
	}
}
