package supervisor

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/stepandel/cantelop/fire-fuse/runtime/microvm"
)

const linuxBootIDPath = "/proc/sys/kernel/random/boot_id"

var ErrInvalidProcStat = errors.New("invalid Linux process stat record")

type readFileFunc func(string) ([]byte, error)

type processIdentityStatus string

const (
	processIdentityMatch         processIdentityStatus = "match"
	processIdentityMissing       processIdentityStatus = "missing"
	processIdentityDifferentBoot processIdentityStatus = "different_boot"
	processIdentityReusedPID     processIdentityStatus = "reused_pid"
)

// readLinuxProcessIdentity combines the current boot ID with a process start
// time. Together they disambiguate a PID reused after process exit or reboot.
func readLinuxProcessIdentity(pid int) (microvm.ProcessIdentity, error) {
	return readProcessIdentity(pid, os.ReadFile)
}

// verifyLinuxProcessIdentity compares persisted identity with the current
// Linux process table without signaling the numeric PID.
func verifyLinuxProcessIdentity(expected microvm.ProcessIdentity) (processIdentityStatus, error) {
	return verifyProcessIdentity(expected, os.ReadFile)
}

func verifyProcessIdentity(
	expected microvm.ProcessIdentity,
	readFile readFileFunc,
) (processIdentityStatus, error) {
	if expected == (microvm.ProcessIdentity{}) {
		return "", fmt.Errorf("verify process identity: %w: identity is empty", microvm.ErrInvalidRuntimeOwnership)
	}
	if err := expected.Validate(); err != nil {
		return "", fmt.Errorf("verify process identity: %w", err)
	}

	bootIDBytes, err := readFile(linuxBootIDPath)
	if err != nil {
		return "", fmt.Errorf("read Linux boot ID: %w", err)
	}
	currentBootID := strings.TrimSpace(string(bootIDBytes))
	currentBoot := expected
	currentBoot.BootID = currentBootID
	if err := currentBoot.Validate(); err != nil {
		return "", fmt.Errorf("validate current Linux boot ID: %w", err)
	}
	if currentBootID != expected.BootID {
		return processIdentityDifferentBoot, nil
	}

	statPath := fmt.Sprintf("/proc/%d/stat", expected.PID)
	statBytes, err := readFile(statPath)
	if errors.Is(err, os.ErrNotExist) {
		return processIdentityMissing, nil
	}
	if err != nil {
		return "", fmt.Errorf("read Linux process stat: %w", err)
	}
	startTime, err := parseProcessStartTime(expected.PID, string(statBytes))
	if err != nil {
		return "", err
	}
	if startTime != expected.StartTimeTicks {
		return processIdentityReusedPID, nil
	}
	return processIdentityMatch, nil
}

func readProcessIdentity(pid int, readFile readFileFunc) (microvm.ProcessIdentity, error) {
	if pid < 1 {
		return microvm.ProcessIdentity{}, fmt.Errorf("read process identity: PID must be positive")
	}
	bootIDBytes, err := readFile(linuxBootIDPath)
	if err != nil {
		return microvm.ProcessIdentity{}, fmt.Errorf("read Linux boot ID: %w", err)
	}
	statPath := fmt.Sprintf("/proc/%d/stat", pid)
	statBytes, err := readFile(statPath)
	if err != nil {
		return microvm.ProcessIdentity{}, fmt.Errorf("read Linux process stat: %w", err)
	}
	startTime, err := parseProcessStartTime(pid, string(statBytes))
	if err != nil {
		return microvm.ProcessIdentity{}, err
	}
	identity := microvm.ProcessIdentity{
		PID:            pid,
		BootID:         strings.TrimSpace(string(bootIDBytes)),
		StartTimeTicks: startTime,
	}
	if err := identity.Validate(); err != nil {
		return microvm.ProcessIdentity{}, fmt.Errorf("validate Linux process identity: %w", err)
	}
	return identity, nil
}

// parseProcessStartTime reads field 22 from /proc/PID/stat. The second field is
// parenthesized and may itself contain spaces or closing parentheses, so fields
// can only be split after its final ") " delimiter.
func parseProcessStartTime(pid int, stat string) (uint64, error) {
	prefix := strconv.Itoa(pid) + " "
	if !strings.HasPrefix(stat, prefix) {
		return 0, fmt.Errorf("%w: process ID does not match", ErrInvalidProcStat)
	}
	remainder := strings.TrimSpace(strings.TrimPrefix(stat, prefix))
	if !strings.HasPrefix(remainder, "(") {
		return 0, fmt.Errorf("%w: missing process name", ErrInvalidProcStat)
	}
	nameEnd := strings.LastIndex(remainder, ") ")
	if nameEnd < 1 {
		return 0, fmt.Errorf("%w: unterminated process name", ErrInvalidProcStat)
	}
	fields := strings.Fields(remainder[nameEnd+2:])
	const startTimeIndexAfterName = 19
	if len(fields) <= startTimeIndexAfterName {
		return 0, fmt.Errorf("%w: missing process start time", ErrInvalidProcStat)
	}
	startTime, err := strconv.ParseUint(fields[startTimeIndexAfterName], 10, 64)
	if err != nil || startTime == 0 {
		return 0, fmt.Errorf("%w: invalid process start time", ErrInvalidProcStat)
	}
	return startTime, nil
}
