// Package nfs manages host exports for persistent microVM workspaces.
package nfs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/stepandel/cantelop/fire-fuse/telemetry/warmuptrace"

	"github.com/stepandel/cantelop/fire-fuse/runtime/model"
)

const workspaceExportOptions = "rw,async,subtree_check,all_squash"

var (
	ErrInvalidConfig      = errors.New("invalid NFS export manager configuration")
	ErrInvalidGuestIP     = errors.New("invalid NFS export guest IP")
	ErrWorkspaceRoot      = errors.New("unsafe NFS workspace root")
	ErrWorkspacePath      = errors.New("unsafe NFS workspace path")
	ErrWorkspaceUnmounted = errors.New("NFS workspace root is not mounted")
	ErrExportNotFound     = errors.New("NFS workspace export not found")
)

type commandRunner func(context.Context, string, ...string) (string, error)

// Manager creates exact host workspace directories and client-specific exports.
type Manager struct {
	exportfsBin   string
	juicefsBin    string
	mountpointBin string
	workspaceRoot string
	adminRoot     string
	run           commandRunner
	chown         func(string, int, int) error
	checkMount    func(string) error
}

// NewManager constructs an NFS workspace export manager.
func NewManager(exportfsBin, juicefsBin, mountpointBin, workspaceRoot, adminRoot string) (*Manager, error) {
	return newManager(exportfsBin, juicefsBin, mountpointBin, workspaceRoot, adminRoot, runCommand)
}

func newManager(
	exportfsBin, juicefsBin, mountpointBin, workspaceRoot, adminRoot string,
	run commandRunner,
) (*Manager, error) {
	for name, path := range map[string]string{
		"exportfs binary":   exportfsBin,
		"JuiceFS binary":    juicefsBin,
		"mountpoint binary": mountpointBin,
		"workspace root":    workspaceRoot,
		"admin root":        adminRoot,
	} {
		if !isCleanAbsolutePath(path) {
			return nil, fmt.Errorf("%w: %s must be a clean absolute path", ErrInvalidConfig, name)
		}
	}
	if run == nil {
		return nil, fmt.Errorf("%w: command runner is required", ErrInvalidConfig)
	}
	if workspaceRoot == adminRoot {
		return nil, fmt.Errorf("%w: workspace and admin roots must differ", ErrInvalidConfig)
	}
	return &Manager{
		exportfsBin:   exportfsBin,
		juicefsBin:    juicefsBin,
		mountpointBin: mountpointBin,
		workspaceRoot: workspaceRoot,
		adminRoot:     adminRoot,
		run:           run,
		chown:         os.Chown,
		checkMount:    CheckWorkspaceMount,
	}, nil
}

// Purge permanently removes one archived Workspace through JuiceFS. The
// directory is first moved out of the routable namespace so an interrupted
// purge can be retried without exposing partially removed data.
func (m *Manager) Purge(ctx context.Context, workspaceID string) error {
	if err := model.ValidateWorkspaceID(workspaceID); err != nil {
		return err
	}
	if err := requireDirectory(m.workspaceRoot, ErrWorkspaceRoot); err != nil {
		return err
	}
	if err := requireDirectory(m.adminRoot, ErrWorkspaceRoot); err != nil {
		return err
	}
	if _, err := m.run(ctx, m.mountpointBin, "--quiet", m.workspaceRoot); err != nil {
		return errors.Join(ErrWorkspaceUnmounted, err)
	}
	if _, err := m.run(ctx, m.mountpointBin, "--quiet", m.adminRoot); err != nil {
		return errors.Join(ErrWorkspaceUnmounted, err)
	}
	if err := m.checkMount(m.workspaceRoot); err != nil {
		return err
	}

	workspacePath := filepath.Join(m.workspaceRoot, workspaceID)
	quarantinePath := filepath.Join(m.workspaceRoot, ".purging-"+workspaceID)
	adminQuarantinePath := filepath.Join(m.adminRoot, ".purging-"+workspaceID)
	workspaceExists, err := directoryExists(workspacePath)
	if err != nil {
		return fmt.Errorf("inspect Workspace purge source: %w", err)
	}
	quarantineExists, err := directoryExists(quarantinePath)
	if err != nil {
		return fmt.Errorf("inspect Workspace purge target: %w", err)
	}
	if workspaceExists && quarantineExists {
		return fmt.Errorf("%w: Workspace exists in active and purge namespaces", ErrWorkspacePath)
	}
	if workspaceExists {
		if err := os.Rename(workspacePath, quarantinePath); err != nil {
			return fmt.Errorf("quarantine Workspace: %w", err)
		}
		quarantineExists = true
	}
	if !quarantineExists {
		return nil
	}
	if _, err := m.run(ctx, m.juicefsBin, "rmr", "--skip-trash", adminQuarantinePath); err != nil {
		return fmt.Errorf("purge Workspace through JuiceFS: %w", err)
	}
	if exists, err := directoryExists(quarantinePath); err != nil {
		return fmt.Errorf("verify Workspace purge: %w", err)
	} else if exists {
		return fmt.Errorf("verify Workspace purge: %w", ErrWorkspacePath)
	}
	return nil
}

func directoryExists(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, ErrWorkspacePath
	}
	return true, nil
}

// Export ensures one persistent workspace directory exists and exports only
// that directory to the exact guest IPv4 address.
func (m *Manager) Export(
	ctx context.Context,
	workspaceID string,
	guestIP netip.Addr,
	uid, gid uint32,
) (string, error) {
	if uid == 0 || gid == 0 {
		return "", fmt.Errorf("%w: workspace identity must be non-root", ErrInvalidConfig)
	}
	finishPath := warmuptrace.Start(ctx, "workspace.nfs.derive_export_path")
	workspacePath, err := m.workspacePath(workspaceID, guestIP)
	finishPath()
	if err != nil {
		return "", err
	}
	finishRoot := warmuptrace.Start(ctx, "workspace.nfs.validate_workspace_root")
	if err := requireDirectory(m.workspaceRoot, ErrWorkspaceRoot); err != nil {
		finishRoot()
		return "", err
	}
	finishRoot()
	finishMountpoint := warmuptrace.Start(ctx, "workspace.nfs.verify_backing_mount")
	if _, err := m.run(ctx, m.mountpointBin, "--quiet", m.workspaceRoot); err != nil {
		finishMountpoint()
		return "", errors.Join(ErrWorkspaceUnmounted, err)
	}
	finishMountpoint()
	if err := m.checkMount(m.workspaceRoot); err != nil {
		return "", err
	}

	finishDirectory := warmuptrace.Start(ctx, "workspace.nfs.create_workspace_directory")
	if err := os.Mkdir(workspacePath, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		finishDirectory()
		return "", fmt.Errorf("create NFS workspace directory: %w", err)
	}
	if err := requireDirectory(workspacePath, ErrWorkspacePath); err != nil {
		finishDirectory()
		return "", err
	}
	if err := m.chown(workspacePath, int(uid), int(gid)); err != nil {
		finishDirectory()
		return "", fmt.Errorf("assign NFS workspace ownership: %w", err)
	}
	if err := os.Chmod(workspacePath, 0o700); err != nil {
		finishDirectory()
		return "", fmt.Errorf("enforce NFS workspace permissions: %w", err)
	}
	finishDirectory()

	target := exportTarget(guestIP, workspacePath)
	finishExport := warmuptrace.Start(ctx, "workspace.nfs.publish_export")
	if _, err := m.run(
		ctx,
		m.exportfsBin,
		"-i",
		"-o",
		workspaceExportOptions+",anonuid="+strconv.FormatUint(uint64(uid), 10)+",anongid="+strconv.FormatUint(uint64(gid), 10)+","+workspaceFSIDOption(workspaceID),
		target,
	); err != nil {
		finishExport()
		return "", fmt.Errorf("export NFS workspace: %w", err)
	}
	finishExport()
	return workspacePath, nil
}

// Unexport removes only one guest's export entry. Persistent workspace data
// and exports of the same workspace to other guests remain untouched.
func (m *Manager) Unexport(
	ctx context.Context,
	workspaceID string,
	guestIP netip.Addr,
) error {
	workspacePath, err := m.workspacePath(workspaceID, guestIP)
	if err != nil {
		return err
	}
	target := exportTarget(guestIP, workspacePath)
	if _, err := m.run(ctx, m.exportfsBin, "-u", target); err != nil {
		if errors.Is(err, ErrExportNotFound) {
			return nil
		}
		return fmt.Errorf("unexport NFS workspace: %w", err)
	}
	return nil
}

func (m *Manager) workspacePath(workspaceID string, guestIP netip.Addr) (string, error) {
	if err := model.ValidateWorkspaceID(workspaceID); err != nil {
		return "", err
	}
	if !guestIP.IsValid() || !guestIP.Is4() || !guestIP.IsPrivate() {
		return "", fmt.Errorf("%w: %q", ErrInvalidGuestIP, guestIP)
	}
	return filepath.Join(m.workspaceRoot, workspaceID), nil
}

func exportTarget(guestIP netip.Addr, workspacePath string) string {
	return guestIP.String() + ":" + workspacePath
}

func workspaceFSIDOption(workspaceID string) string {
	sum := sha256.Sum256([]byte(workspaceID))
	return fmt.Sprintf(
		"fsid=%x-%x-%x-%x-%x",
		sum[0:4],
		sum[4:6],
		sum[6:8],
		sum[8:10],
		sum[10:16],
	)
}

func requireDirectory(path string, sentinel error) error {
	info, err := os.Lstat(path)
	if err != nil {
		return errors.Join(sentinel, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%w: %s must be a directory, not a symlink", sentinel, path)
	}
	return nil
}

func isCleanAbsolutePath(path string) bool {
	return filepath.IsAbs(path) &&
		filepath.Clean(path) == path &&
		path != string(filepath.Separator)
}

func runCommand(ctx context.Context, name string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Env = append(os.Environ(), "LC_ALL=C")
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	err := command.Run()
	detail := strings.TrimSpace(output.String())
	if err == nil {
		return detail, nil
	}
	if strings.Contains(detail, "not exported") ||
		strings.Contains(detail, "Could not find") {
		return detail, errors.Join(ErrExportNotFound, err)
	}
	if detail == "" {
		return "", err
	}
	return detail, fmt.Errorf("%w: %s", err, detail)
}
