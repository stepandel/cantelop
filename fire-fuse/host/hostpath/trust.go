// Package hostpath validates filesystem paths used by the privileged service.
package hostpath

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

var (
	ErrUntrustedDirectory = errors.New("untrusted host directory")
	ErrUntrustedFile      = errors.New("untrusted host file")
)

type directoryInfo struct {
	isDirectory bool
	isSymlink   bool
	mode        os.FileMode
	uid         uint32
}

type pathResolver func(string) (string, error)
type directoryInspector func(string) (directoryInfo, error)

// CheckTrustedDirectory resolves a configured directory, then requires the
// directory and every resolved ancestor to be root-owned and not writable by
// group or other users.
func CheckTrustedDirectory(field, path string) error {
	return checkTrustedDirectory(field, path, 0, filepath.EvalSymlinks, inspectDirectory)
}

// CheckTrustedFile resolves a configured file, requires both its configured
// and resolved parent hierarchies to be trusted, and then requires the resolved
// file to be root-owned, regular, and not writable by group or other users.
func CheckTrustedFile(field, path string) error {
	return checkTrustedFile(field, path, 0, filepath.EvalSymlinks, inspectDirectory)
}

func checkTrustedFile(
	field string,
	path string,
	trustedUID uint32,
	resolve pathResolver,
	inspect directoryInspector,
) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("%w: %s must be a clean absolute path", ErrUntrustedFile, field)
	}
	if err := checkDirectoryHierarchy(field, filepath.Dir(path), trustedUID, true, inspect); err != nil {
		return fmt.Errorf("%w: %w", ErrUntrustedFile, err)
	}
	resolved, err := resolve(path)
	if err != nil {
		return fmt.Errorf("%w: resolve %s %s: %w", ErrUntrustedFile, field, path, err)
	}
	if !filepath.IsAbs(resolved) || filepath.Clean(resolved) != resolved {
		return fmt.Errorf("%w: resolved %s path is not clean and absolute", ErrUntrustedFile, field)
	}
	if err := checkDirectoryHierarchy(field, filepath.Dir(resolved), trustedUID, false, inspect); err != nil {
		return fmt.Errorf("%w: %w", ErrUntrustedFile, err)
	}
	info, err := inspect(resolved)
	if err != nil {
		return fmt.Errorf("%w: inspect resolved %s file %s: %w", ErrUntrustedFile, field, resolved, err)
	}
	if !info.mode.IsRegular() {
		return fmt.Errorf("%w: resolved %s file %s is not regular", ErrUntrustedFile, field, resolved)
	}
	if info.uid != trustedUID {
		return fmt.Errorf(
			"%w: resolved %s file %s is owned by UID %d, want %d",
			ErrUntrustedFile,
			field,
			resolved,
			info.uid,
			trustedUID,
		)
	}
	if info.mode.Perm()&0o022 != 0 {
		return fmt.Errorf(
			"%w: resolved %s file %s is group or world writable with mode %04o",
			ErrUntrustedFile,
			field,
			resolved,
			info.mode.Perm(),
		)
	}
	return nil
}

func checkTrustedDirectory(
	field string,
	path string,
	trustedUID uint32,
	resolve pathResolver,
	inspect directoryInspector,
) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("%w: %s must be a clean absolute path", ErrUntrustedDirectory, field)
	}
	if err := checkDirectoryHierarchy(field, path, trustedUID, true, inspect); err != nil {
		return err
	}
	resolved, err := resolve(path)
	if err != nil {
		return fmt.Errorf("%w: resolve %s %s: %w", ErrUntrustedDirectory, field, path, err)
	}
	if !filepath.IsAbs(resolved) || filepath.Clean(resolved) != resolved {
		return fmt.Errorf("%w: resolved %s path is not clean and absolute", ErrUntrustedDirectory, field)
	}
	return checkDirectoryHierarchy(field, resolved, trustedUID, false, inspect)
}

func checkDirectoryHierarchy(
	field, path string,
	trustedUID uint32,
	allowSymlinks bool,
	inspect directoryInspector,
) error {
	for current := path; ; current = filepath.Dir(current) {
		info, err := inspect(current)
		if err != nil {
			return fmt.Errorf("%w: inspect %s directory %s: %w", ErrUntrustedDirectory, field, current, err)
		}
		if info.isSymlink && allowSymlinks {
			if current == string(filepath.Separator) {
				return fmt.Errorf("%w: filesystem root cannot be a symlink", ErrUntrustedDirectory)
			}
			continue
		}
		if !info.isDirectory {
			return fmt.Errorf("%w: %s path component %s is not a directory", ErrUntrustedDirectory, field, current)
		}
		if info.uid != trustedUID {
			return fmt.Errorf(
				"%w: %s directory %s is owned by UID %d, want %d",
				ErrUntrustedDirectory,
				field,
				current,
				info.uid,
				trustedUID,
			)
		}
		if info.mode.Perm()&0o022 != 0 {
			return fmt.Errorf(
				"%w: %s directory %s is group or world writable with mode %04o",
				ErrUntrustedDirectory,
				field,
				current,
				info.mode.Perm(),
			)
		}
		if current == string(filepath.Separator) {
			break
		}
	}
	return nil
}

func inspectDirectory(path string) (directoryInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return directoryInfo{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return directoryInfo{}, errors.New("filesystem ownership metadata is unavailable")
	}
	return directoryInfo{
		isDirectory: info.IsDir(),
		isSymlink:   info.Mode()&os.ModeSymlink != 0,
		mode:        info.Mode(),
		uid:         stat.Uid,
	}, nil
}
