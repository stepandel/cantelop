package materializer

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	maxExtractedRootFSEntries = 1_000_000
	maxExtractedRootFSBytes   = int64(40 << 30)
)

func extractRootFS(ctx context.Context, archivePath, root string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open exported rootfs: %w", err)
	}
	defer file.Close()
	reader := tar.NewReader(file)
	seen := make(map[string]struct{})
	var expandedBytes int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read exported rootfs: %w", err)
		}
		name := strings.TrimSuffix(strings.TrimPrefix(header.Name, "./"), "/")
		if !safeArchivePath(name) {
			return errors.New("exported rootfs contains an unsafe path")
		}
		if _, exists := seen[name]; exists {
			return fmt.Errorf("exported rootfs contains duplicate path %q", name)
		}
		seen[name] = struct{}{}
		if len(seen) > maxExtractedRootFSEntries {
			return errors.New("exported rootfs contains too many entries")
		}
		if header.Size < 0 || expandedBytes > maxExtractedRootFSBytes-header.Size {
			return errors.New("exported rootfs exceeds expanded byte limit")
		}
		expandedBytes += header.Size
		target := filepath.Join(root, filepath.FromSlash(name))
		if err := ensureSafeParents(root, filepath.Dir(target)); err != nil {
			return err
		}
		mode := os.FileMode(header.Mode & 0o7777)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.Mkdir(target, mode.Perm()); err != nil && !errors.Is(err, os.ErrExist) {
				return fmt.Errorf("create rootfs directory: %w", err)
			}
			if err := os.Chmod(target, mode.Perm()); err != nil {
				return fmt.Errorf("set rootfs directory mode: %w", err)
			}
		case tar.TypeReg, tar.TypeRegA:
			output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode.Perm())
			if err != nil {
				return fmt.Errorf("create rootfs file: %w", err)
			}
			_, copyErr := io.Copy(output, reader)
			modeErr := output.Chmod(mode.Perm())
			closeErr := output.Close()
			if copyErr != nil || modeErr != nil || closeErr != nil {
				return errors.Join(copyErr, modeErr, closeErr)
			}
		case tar.TypeSymlink:
			if header.Linkname == "" || strings.ContainsRune(header.Linkname, '\x00') {
				return errors.New("exported rootfs contains an invalid symlink")
			}
			if err := os.Symlink(header.Linkname, target); err != nil {
				return fmt.Errorf("create rootfs symlink: %w", err)
			}
		case tar.TypeLink:
			linkName := strings.TrimPrefix(header.Linkname, "./")
			if !safeArchivePath(linkName) {
				return errors.New("exported rootfs contains an invalid hard link")
			}
			linkTarget := filepath.Join(root, filepath.FromSlash(linkName))
			if err := ensureWithinRoot(root, linkTarget); err != nil {
				return err
			}
			if err := ensureSafeParents(root, filepath.Dir(linkTarget)); err != nil {
				return err
			}
			info, err := os.Lstat(linkTarget)
			if err != nil || !info.Mode().IsRegular() {
				return errors.New("exported rootfs hard link target must be an existing regular file")
			}
			if err := os.Link(linkTarget, target); err != nil {
				return fmt.Errorf("create rootfs hard link: %w", err)
			}
		default:
			return fmt.Errorf("exported rootfs contains unsupported entry type %d", header.Typeflag)
		}
		if os.Geteuid() == 0 && header.Typeflag != tar.TypeSymlink {
			if err := os.Chown(target, header.Uid, header.Gid); err != nil {
				return fmt.Errorf("set rootfs ownership: %w", err)
			}
		}
	}
}

// normalizeRootFSTimestamps removes host wall-clock time from the directory
// passed to mkfs.ext4. Directories are processed after their children because
// changing a child mutates its parent's timestamp. Symlinks are changed
// without following them so an image cannot redirect normalization elsewhere.
func normalizeRootFSTimestamps(root string) error {
	paths := make([]string, 0)
	if err := filepath.Walk(root, func(path string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		paths = append(paths, path)
		return nil
	}); err != nil {
		return fmt.Errorf("enumerate rootfs timestamps: %w", err)
	}
	timestamps := []unix.Timespec{
		{Sec: deterministicBuildUnixSecond},
		{Sec: deterministicBuildUnixSecond},
	}
	for index := len(paths) - 1; index >= 0; index-- {
		if err := unix.UtimesNanoAt(unix.AT_FDCWD, paths[index], timestamps, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return fmt.Errorf("normalize rootfs timestamp: %w", err)
		}
	}
	return nil
}

func ensureSafeParents(root, parent string) error {
	if err := ensureWithinRoot(root, parent); err != nil {
		return err
	}
	relative, _ := filepath.Rel(root, parent)
	current := root
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		if component == "." || component == "" {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0o755); err != nil {
				return err
			}
			continue
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("rootfs path traverses a non-directory parent")
		}
	}
	return nil
}

func ensureWithinRoot(root, target string) error {
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("rootfs path escapes extraction directory")
	}
	return nil
}
