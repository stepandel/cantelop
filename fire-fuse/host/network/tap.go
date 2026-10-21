// Package network manages host networking resources owned by microVMs.
package network

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/telemetry/warmuptrace"
)

const (
	linuxInterfaceNameMax = 15
	tapNamePrefix         = "ff"
	tapAliasPrefix        = "fire-fuse:"
	tapRollbackTimeout    = 5 * time.Second

	// SnapshotTemplateVMID owns the TAP the snapshot builder creates beside a
	// live fire-fuse service.
	SnapshotTemplateVMID = "snapshot-template"
)

var (
	ErrInvalidTAPConfig    = errors.New("invalid TAP manager configuration")
	ErrInvalidTAPOwnership = errors.New("invalid TAP ownership marker")
	ErrInvalidVMID         = errors.New("invalid microVM ID")
	ErrTAPNotFound         = errors.New("TAP device not found")
	vmIDPattern            = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,63}$`)
	managedTAPPattern      = regexp.MustCompile(fmt.Sprintf(
		`^%s[0-9a-f]{%d}$`,
		regexp.QuoteMeta(tapNamePrefix),
		linuxInterfaceNameMax-len(tapNamePrefix),
	))
)

type commandRunner func(context.Context, string, ...string) error
type tapAliasReader func(string) (string, error)

// TAPManager creates and removes Linux TAP devices using iproute2.
type TAPManager struct {
	ipBin     string
	run       commandRunner
	readAlias tapAliasReader
}

// NewTAPManager constructs a production TAP manager.
func NewTAPManager(ipBin string) (*TAPManager, error) {
	return newTAPManager(ipBin, runCommand)
}

func newTAPManager(ipBin string, run commandRunner) (*TAPManager, error) {
	if !filepath.IsAbs(ipBin) || filepath.Clean(ipBin) != ipBin || ipBin == string(filepath.Separator) {
		return nil, fmt.Errorf("%w: ip binary must be a clean absolute file path", ErrInvalidTAPConfig)
	}
	if run == nil {
		return nil, fmt.Errorf("%w: command runner is required", ErrInvalidTAPConfig)
	}
	return &TAPManager{ipBin: ipBin, run: run, readAlias: ReadTAPAlias}, nil
}

// TAPName derives a stable Linux interface name without embedding caller text.
func TAPName(vmID string) (string, error) {
	if !vmIDPattern.MatchString(vmID) {
		return "", fmt.Errorf("%w: %q", ErrInvalidVMID, vmID)
	}
	digest := sha256.Sum256([]byte(vmID))
	encoded := hex.EncodeToString(digest[:])
	name := tapNamePrefix + encoded[:linuxInterfaceNameMax-len(tapNamePrefix)]
	return name, nil
}

// IsManagedTAPName reports whether a host interface is in fire-fuse's reserved
// deterministic TAP namespace.
func IsManagedTAPName(name string) bool {
	return managedTAPPattern.MatchString(name)
}

// TAPAlias returns the positive ownership marker for one validated VM ID.
func TAPAlias(vmID string) (string, error) {
	if !vmIDPattern.MatchString(vmID) {
		return "", fmt.Errorf("%w: %q", ErrInvalidVMID, vmID)
	}
	return tapAliasPrefix + vmID, nil
}

// ValidateTAPOwnership verifies that an alias VM ID derives the interface name.
func ValidateTAPOwnership(name, alias string) error {
	if !strings.HasPrefix(alias, tapAliasPrefix) {
		return ErrInvalidTAPOwnership
	}
	vmID := strings.TrimPrefix(alias, tapAliasPrefix)
	expectedName, err := TAPName(vmID)
	if err != nil || expectedName != name {
		return ErrInvalidTAPOwnership
	}
	return nil
}

// ReadTAPAlias reads one managed interface's Linux ownership alias.
func ReadTAPAlias(name string) (string, error) {
	return readTAPAliasAt("/sys/class/net", name)
}

func readTAPAliasAt(networkClassPath, name string) (string, error) {
	if !IsManagedTAPName(name) {
		return "", fmt.Errorf("%w: unmanaged interface name %q", ErrInvalidTAPOwnership, name)
	}
	alias, err := os.ReadFile(filepath.Join(networkClassPath, name, "ifalias"))
	if err != nil {
		return "", fmt.Errorf("read TAP %s alias: %w", name, err)
	}
	return strings.TrimSpace(string(alias)), nil
}

// Create creates an owned TAP device, assigns its host address, and brings it up.
func (m *TAPManager) Create(
	ctx context.Context,
	vmID string,
	hostAddress netip.Prefix,
	uid, gid int,
) (string, error) {
	if err := validateHostAddress(hostAddress); err != nil {
		return "", err
	}
	return m.create(ctx, vmID, &hostAddress, uid, gid)
}

// CreateUnaddressed creates an owned TAP for a generic snapshot template. The
// template guest never configures IP, so reserving a production subnet would
// be unnecessary and could collide with active allocations.
func (m *TAPManager) CreateUnaddressed(
	ctx context.Context,
	vmID string,
	uid, gid int,
) (string, error) {
	return m.create(ctx, vmID, nil, uid, gid)
}

func (m *TAPManager) create(
	ctx context.Context,
	vmID string,
	hostAddress *netip.Prefix,
	uid, gid int,
) (string, error) {
	finishValidation := warmuptrace.Start(ctx, "network.tap.validate")
	name, err := TAPName(vmID)
	if err != nil {
		finishValidation()
		return "", err
	}
	if uid < 1 || gid < 1 {
		finishValidation()
		return "", fmt.Errorf("%w: TAP owner must use a non-root UID and GID", ErrInvalidTAPConfig)
	}
	alias, err := TAPAlias(vmID)
	if err != nil {
		finishValidation()
		return "", err
	}
	finishValidation()

	finishCreate := warmuptrace.Start(ctx, "network.tap.create_device")
	if err := m.run(
		ctx,
		m.ipBin,
		"tuntap", "add", "dev", name, "mode", "tap",
		"user", strconv.Itoa(uid), "group", strconv.Itoa(gid),
	); err != nil {
		finishCreate()
		return "", fmt.Errorf("create TAP %s: %w", name, err)
	}
	finishCreate()
	finishAlias := warmuptrace.Start(ctx, "network.tap.set_ownership_alias")
	if err := m.run(ctx, m.ipBin, "link", "set", "dev", name, "alias", alias); err != nil {
		finishAlias()
		return "", m.rollbackCreate(ctx, vmID, fmt.Errorf("mark TAP %s ownership: %w", name, err))
	}
	finishAlias()
	if hostAddress != nil {
		finishAddress := warmuptrace.Start(ctx, "network.tap.assign_host_address")
		if err := m.run(ctx, m.ipBin, "addr", "add", hostAddress.String(), "dev", name); err != nil {
			finishAddress()
			return "", m.rollbackCreate(ctx, vmID, fmt.Errorf("assign address to TAP %s: %w", name, err))
		}
		finishAddress()
	}
	finishUp := warmuptrace.Start(ctx, "network.tap.bring_link_up")
	if err := m.run(ctx, m.ipBin, "link", "set", "dev", name, "up"); err != nil {
		finishUp()
		return "", m.rollbackCreate(ctx, vmID, fmt.Errorf("bring TAP %s up: %w", name, err))
	}
	finishUp()
	return name, nil
}

// Delete removes the TAP derived from a VM ID only after its alias proves that
// fire-fuse owns it. Missing devices are successful.
func (m *TAPManager) Delete(ctx context.Context, vmID string) error {
	name, err := TAPName(vmID)
	if err != nil {
		return err
	}
	expectedAlias, err := TAPAlias(vmID)
	if err != nil {
		return err
	}
	alias, err := m.readAlias(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read TAP %s ownership before deletion: %w", name, err)
	}
	if alias != expectedAlias || ValidateTAPOwnership(name, alias) != nil {
		return fmt.Errorf(
			"%w: TAP %s alias %q does not match %q",
			ErrInvalidTAPOwnership,
			name,
			alias,
			expectedAlias,
		)
	}
	return m.deleteByName(ctx, name)
}

// deleteByName is intentionally unverified so creation rollback can remove a
// device when installing its ownership alias was the operation that failed.
func (m *TAPManager) deleteByName(ctx context.Context, name string) error {
	if err := m.run(ctx, m.ipBin, "link", "delete", "dev", name); err != nil {
		if errors.Is(err, ErrTAPNotFound) {
			return nil
		}
		return fmt.Errorf("delete TAP %s: %w", name, err)
	}
	return nil
}

func runCommand(ctx context.Context, name string, args ...string) error {
	_, err := runCommandOutput(ctx, name, args...)
	if err != nil && (strings.Contains(err.Error(), "Cannot find device") || strings.Contains(err.Error(), "does not exist")) {
		return errors.Join(ErrTAPNotFound, err)
	}
	return err
}

func runCommandOutput(ctx context.Context, name string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Env = append(os.Environ(), "LC_ALL=C")
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		detail := strings.TrimSpace(output.String())
		wrapped := fmt.Errorf("run %s: %w", filepath.Base(name), err)
		if detail != "" {
			wrapped = fmt.Errorf("%w: %s", wrapped, detail)
		}
		return detail, wrapped
	}
	return strings.TrimSpace(output.String()), nil
}

func validateHostAddress(address netip.Prefix) error {
	if !address.IsValid() || !address.Addr().Is4() || address.Bits() != microVMSubnetBits {
		return fmt.Errorf("%w: TAP host address must be an IPv4 /30", ErrInvalidTAPConfig)
	}
	if !address.Addr().IsPrivate() {
		return fmt.Errorf("%w: TAP host address must be private", ErrInvalidTAPConfig)
	}
	if address.Addr() != address.Masked().Addr().Next() {
		return fmt.Errorf("%w: TAP host address must be the first usable subnet address", ErrInvalidTAPConfig)
	}
	return nil
}

func (m *TAPManager) rollbackCreate(ctx context.Context, vmID string, cause error) error {
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), tapRollbackTimeout)
	defer cancel()
	name, err := TAPName(vmID)
	if err != nil {
		return errors.Join(cause, wrapCleanupError(err))
	}
	return errors.Join(cause, wrapCleanupError(m.deleteByName(rollbackCtx, name)))
}

func wrapCleanupError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("roll back TAP creation: %w", err)
}
