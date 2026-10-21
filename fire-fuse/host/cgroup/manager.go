// Package cgroup owns the per-Firecracker cgroup v2 hierarchy used for CPU
// scheduling and live resource accounting.
package cgroup

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/stepandel/cantelop/fire-fuse/runtime/sandboxprofile"
)

const (
	maxSelfCgroupBytes = 16 << 10
	// Equal weights let runnable sandboxes share the host's available CPU.
	sandboxCPUWeight   = "100"
	sandboxCPUPeriodUS = 100000
	// Leave 512 MiB for VMM overhead and page cache beyond guest RAM.
	sandboxMemoryMaxBytes = int64(sandboxprofile.DefaultMemoryMiB+sandboxprofile.HotplugMemoryMiB+512) << 20
)

// sandboxCPUMax lets a sandbox consume at most one host core per guest vCPU
// per period.
var sandboxCPUMax = fmt.Sprintf("%d %d", sandboxprofile.DefaultVCPUs*sandboxCPUPeriodUS, sandboxCPUPeriodUS)

const managerCgroupName = "manager"

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,63}$`)

var (
	ErrInvalidHierarchy = errors.New("invalid cgroup v2 hierarchy")
	ErrInvalidUsage     = errors.New("invalid cgroup usage")
)

// Usage is one instantaneous reading of cumulative CPU and current host
// memory charged to a sandbox cgroup.
type Usage struct {
	CPUUsageUS         uint64
	MemoryCurrentBytes uint64
}

// Manager owns a sandboxes subtree beneath the current process's delegated
// cgroup. parent is relative to the cgroup v2 mount for jailer's
// --parent-cgroup argument; directory is its host-visible absolute path.
type Manager struct {
	parent    string
	directory string
}

// NewManager discovers the current unified cgroup from procSelfCgroup and
// creates a stable sandboxes child below it.
func NewManager(procSelfCgroup, mountpoint string) (*Manager, error) {
	if !filepath.IsAbs(procSelfCgroup) || !filepath.IsAbs(mountpoint) || filepath.Clean(mountpoint) != mountpoint {
		return nil, fmt.Errorf("%w: paths must be clean and absolute", ErrInvalidHierarchy)
	}
	document, err := os.ReadFile(procSelfCgroup)
	if err != nil {
		return nil, fmt.Errorf("read current cgroup: %w", err)
	}
	if len(document) > maxSelfCgroupBytes {
		return nil, fmt.Errorf("%w: proc document exceeds %d bytes", ErrInvalidHierarchy, maxSelfCgroupBytes)
	}
	current, err := parseUnifiedPath(string(document))
	if err != nil {
		return nil, err
	}
	currentDirectory := filepath.Join(mountpoint, strings.TrimPrefix(current, string(filepath.Separator)))
	if err := prepareDelegatedHierarchy(currentDirectory); err != nil {
		return nil, err
	}
	parent := filepath.Join(strings.TrimPrefix(current, string(filepath.Separator)), "sandboxes")
	directory := filepath.Join(mountpoint, parent)
	if err := os.Mkdir(directory, 0o750); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("create sandbox cgroup parent: %w", err)
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return nil, fmt.Errorf("inspect sandbox cgroup parent: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: sandbox cgroup parent is not a directory", ErrInvalidHierarchy)
	}
	if err := enableControllers(directory); err != nil {
		return nil, err
	}
	return &Manager{parent: filepath.ToSlash(parent), directory: directory}, nil
}

// prepareDelegatedHierarchy moves the supervisor into its own leaf cgroup so
// the delegated service root can enable controllers for sibling sandbox
// cgroups. Cgroup v2 does not allow a non-root cgroup to contain both processes
// and controller-managed children.
func prepareDelegatedHierarchy(directory string) error {
	info, err := os.Lstat(directory)
	if err != nil {
		return fmt.Errorf("inspect delegated cgroup: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: delegated cgroup is not a directory", ErrInvalidHierarchy)
	}
	controllers, err := os.ReadFile(filepath.Join(directory, "cgroup.controllers"))
	if err != nil {
		return fmt.Errorf("read delegated cgroup controllers: %w", err)
	}
	available := make(map[string]bool)
	for _, controller := range strings.Fields(string(controllers)) {
		available[controller] = true
	}
	if !available["cpu"] || !available["memory"] {
		return fmt.Errorf("%w: delegated cgroup requires cpu and memory controllers", ErrInvalidHierarchy)
	}
	managerDirectory := filepath.Join(directory, managerCgroupName)
	if err := os.Mkdir(managerDirectory, 0o750); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("create manager cgroup: %w", err)
	}
	if err := os.WriteFile(
		filepath.Join(managerDirectory, "cgroup.procs"),
		[]byte(strconv.Itoa(os.Getpid())),
		0o600,
	); err != nil {
		return fmt.Errorf("move supervisor into manager cgroup: %w", err)
	}
	return enableControllers(directory)
}

func enableControllers(directory string) error {
	if err := os.WriteFile(filepath.Join(directory, "cgroup.subtree_control"), []byte("+cpu +memory"), 0o600); err != nil {
		return fmt.Errorf("enable sandbox cgroup controllers: %w", err)
	}
	return nil
}

func parseUnifiedPath(document string) (string, error) {
	var unified string
	scanner := bufio.NewScanner(strings.NewReader(document))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "0::") {
			continue
		}
		if unified != "" {
			return "", fmt.Errorf("%w: multiple unified cgroups", ErrInvalidHierarchy)
		}
		unified = strings.TrimPrefix(line, "0::")
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("scan current cgroup: %w", err)
	}
	if unified == "" || !filepath.IsAbs(unified) || filepath.Clean(unified) != unified || unified == string(filepath.Separator) {
		return "", fmt.Errorf("%w: missing or unsafe unified cgroup", ErrInvalidHierarchy)
	}
	return unified, nil
}

// Parent returns the cgroup-root-relative parent passed to Firecracker jailer.
func (m *Manager) Parent() string { return m.parent }

// Prepare creates an empty cgroup for runtimeID and applies its CPU policy
// before the jailer can enter it. An existing empty directory is accepted so
// interrupted startup can be retried safely.
func (m *Manager) Prepare(runtimeID string) error {
	directory, err := m.runtimeDirectory(runtimeID)
	if err != nil {
		return err
	}
	if err := os.Mkdir(directory, 0o750); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("create sandbox cgroup: %w", err)
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return fmt.Errorf("inspect sandbox cgroup: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: sandbox cgroup is not a directory", ErrInvalidHierarchy)
	}
	for _, setting := range []struct{ name, value string }{
		{"cpu.weight", sandboxCPUWeight},
		{"cpu.max", sandboxCPUMax},
		{"memory.max", strconv.FormatInt(sandboxMemoryMaxBytes, 10)},
	} {
		path := filepath.Join(directory, setting.name)
		if err := os.WriteFile(path, []byte(setting.value), 0o600); err != nil {
			return fmt.Errorf("configure sandbox %s: %w", setting.name, err)
		}
		actual, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("verify sandbox %s: %w", setting.name, err)
		}
		if strings.TrimSpace(string(actual)) != setting.value {
			return fmt.Errorf("%w: sandbox %s is %q, want %q", ErrInvalidHierarchy, setting.name, strings.TrimSpace(string(actual)), setting.value)
		}
	}
	return nil
}

// Remove deletes an empty sandbox cgroup. Missing directories are already
// clean and therefore succeed.
func (m *Manager) Remove(runtimeID string) error {
	directory, err := m.runtimeDirectory(runtimeID)
	if err != nil {
		return err
	}
	if err := os.Remove(directory); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove sandbox cgroup: %w", err)
	}
	return nil
}

// Read returns cumulative CPU microseconds and current memory bytes.
func (m *Manager) Read(runtimeID string) (Usage, error) {
	directory, err := m.runtimeDirectory(runtimeID)
	if err != nil {
		return Usage{}, err
	}
	cpuDocument, err := os.ReadFile(filepath.Join(directory, "cpu.stat"))
	if err != nil {
		return Usage{}, fmt.Errorf("read sandbox cpu.stat: %w", err)
	}
	memoryDocument, err := os.ReadFile(filepath.Join(directory, "memory.current"))
	if err != nil {
		return Usage{}, fmt.Errorf("read sandbox memory.current: %w", err)
	}
	cpu, err := parseCPUUsage(string(cpuDocument))
	if err != nil {
		return Usage{}, err
	}
	memory, err := parseUint(strings.TrimSpace(string(memoryDocument)), "memory.current")
	if err != nil {
		return Usage{}, err
	}
	return Usage{CPUUsageUS: cpu, MemoryCurrentBytes: memory}, nil
}

func (m *Manager) runtimeDirectory(runtimeID string) (string, error) {
	if m == nil || m.directory == "" || !idPattern.MatchString(runtimeID) {
		return "", fmt.Errorf("%w: invalid runtime ID", ErrInvalidHierarchy)
	}
	return filepath.Join(m.directory, runtimeID), nil
}

func parseCPUUsage(document string) (uint64, error) {
	var value string
	for _, line := range strings.Split(document, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "usage_usec" {
			if value != "" {
				return 0, fmt.Errorf("%w: duplicate cpu.stat usage_usec", ErrInvalidUsage)
			}
			value = fields[1]
		}
	}
	if value == "" {
		return 0, fmt.Errorf("%w: cpu.stat omits usage_usec", ErrInvalidUsage)
	}
	return parseUint(value, "cpu.stat usage_usec")
}

func parseUint(value, field string) (uint64, error) {
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %s is not an unsigned integer", ErrInvalidUsage, field)
	}
	return parsed, nil
}
