//go:build linux && amd64

package guestrunner

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	linuxCapabilityVersion3 = 0x20080522
	auditArchX86_64         = 0xc000003e
	x32SyscallBit           = 0x40000000
	seccompDataNumberOffset = 0
	seccompDataArchOffset   = 4
	seccompDataArg0Offset   = 16
)

// rootWorkloadCapabilities is intentionally limited to operations used by
// conventional root-started daemons. In particular it excludes system,
// network-administration, raw-networking, tracing, device, resource, and
// kernel-control capabilities.
//
// CAP_SYS_CHROOT is excluded on purpose. The runner confines the workload with
// chroot alone, and a process that can call chroot again can walk out of that
// confinement through a directory it owns. Privileges are restricted after the
// runner has already entered the container root, so the workload never needs
// the capability itself.
var rootWorkloadCapabilities = []int{
	unix.CAP_CHOWN,
	unix.CAP_DAC_OVERRIDE,
	unix.CAP_FOWNER,
	unix.CAP_FSETID,
	unix.CAP_KILL,
	unix.CAP_SETGID,
	unix.CAP_SETUID,
	unix.CAP_NET_BIND_SERVICE,
	unix.CAP_AUDIT_WRITE,
}

// deniedWorkloadSyscalls is a second, independent boundary around privileged
// control-plane operations. The capability policy already prevents most of
// these calls; seccomp keeps them unavailable if that policy later regresses.
var deniedWorkloadSyscalls = []uint32{
	// Mount and filesystem topology. chroot is denied because repeating it is
	// the standard way out of a chroot-only container root.
	unix.SYS_MOUNT,
	unix.SYS_UMOUNT2,
	unix.SYS_PIVOT_ROOT,
	unix.SYS_CHROOT,
	unix.SYS_MOVE_MOUNT,
	unix.SYS_OPEN_TREE,
	unix.SYS_FSOPEN,
	unix.SYS_FSCONFIG,
	unix.SYS_FSMOUNT,
	unix.SYS_MOUNT_SETATTR,
	unix.SYS_OPEN_BY_HANDLE_AT,

	// Kernel replacement, modules, and machine control.
	unix.SYS_INIT_MODULE,
	unix.SYS_FINIT_MODULE,
	unix.SYS_DELETE_MODULE,
	unix.SYS_KEXEC_LOAD,
	unix.SYS_KEXEC_FILE_LOAD,
	unix.SYS_REBOOT,
	unix.SYS_SWAPON,
	unix.SYS_SWAPOFF,

	// Namespace and system identity control. clone/clone3 remain available for
	// ordinary processes and language runtimes.
	unix.SYS_UNSHARE,
	unix.SYS_SETNS,
	unix.SYS_SETHOSTNAME,
	unix.SYS_SETDOMAINNAME,

	// Host time and accounting.
	unix.SYS_SETTIMEOFDAY,
	unix.SYS_CLOCK_SETTIME,
	unix.SYS_ADJTIMEX,
	unix.SYS_CLOCK_ADJTIME,
	unix.SYS_ACCT,

	// Kernel inspection, tracing, and programmable kernel interfaces.
	unix.SYS_PTRACE,
	unix.SYS_PROCESS_VM_READV,
	unix.SYS_PROCESS_VM_WRITEV,
	unix.SYS_PERF_EVENT_OPEN,
	unix.SYS_BPF,
	unix.SYS_KCMP,
	unix.SYS_LOOKUP_DCOOKIE,
	unix.SYS_SYSLOG,
	unix.SYS_FANOTIFY_INIT,
	unix.SYS_USERFAULTFD,

	// Direct hardware privilege.
	unix.SYS_IOPL,
	unix.SYS_IOPERM,
}

func restrictWorkloadPrivileges(uid, gid uint32) error {
	lastCapability, err := readLastCapability()
	if err != nil {
		return fmt.Errorf("read kernel capability range: %w", err)
	}
	allowed := capabilityMask(rootWorkloadCapabilities)
	boundingAllowed := allowed
	if uid != 0 {
		boundingAllowed = [2]uint32{}
	}
	for capability := 0; capability <= lastCapability; capability++ {
		if capabilityAllowed(boundingAllowed, capability) {
			continue
		}
		if err := unix.Prctl(unix.PR_CAPBSET_DROP, uintptr(capability), 0, 0, 0); err != nil {
			return fmt.Errorf("drop capability %d from bounding set: %w", capability, err)
		}
	}

	if err := unix.Setgroups(nil); err != nil {
		return fmt.Errorf("clear supplementary groups: %w", err)
	}
	if err := unix.Setgid(int(gid)); err != nil {
		return fmt.Errorf("set gid %d: %w", gid, err)
	}
	if err := unix.Setuid(int(uid)); err != nil {
		return fmt.Errorf("set uid %d: %w", uid, err)
	}

	finalCapabilities := [2]uint32{}
	if uid == 0 {
		finalCapabilities = allowed
	}
	data := [2]unix.CapUserData{
		{
			Effective: finalCapabilities[0],
			Permitted: finalCapabilities[0],
		},
		{
			Effective: finalCapabilities[1],
			Permitted: finalCapabilities[1],
		},
	}
	header := unix.CapUserHeader{
		Version: linuxCapabilityVersion3,
		Pid:     0,
	}
	if err := unix.Capset(&header, &data[0]); err != nil {
		return fmt.Errorf("set final capability set: %w", err)
	}

	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("set no_new_privs: %w", err)
	}
	if err := installWorkloadSeccompFilter(); err != nil {
		return fmt.Errorf("install seccomp filter: %w", err)
	}
	return nil
}

func readLastCapability() (int, error) {
	value, err := os.ReadFile("/proc/sys/kernel/cap_last_cap")
	if err != nil {
		return 0, err
	}
	last, err := strconv.Atoi(strings.TrimSpace(string(value)))
	if err != nil {
		return 0, err
	}
	// Capability ABI v3 has two 32-bit words. Fail closed if a future kernel
	// expands the ABI instead of silently retaining unknown bounding bits.
	if last < 0 || last >= 64 {
		return 0, fmt.Errorf("unsupported cap_last_cap %d", last)
	}
	return last, nil
}

func capabilityMask(capabilities []int) [2]uint32 {
	var mask [2]uint32
	for _, capability := range capabilities {
		mask[capability/32] |= uint32(1) << uint(capability%32)
	}
	return mask
}

func capabilityAllowed(mask [2]uint32, capability int) bool {
	return mask[capability/32]&(uint32(1)<<uint(capability%32)) != 0
}

func installWorkloadSeccompFilter() error {
	filters, err := workloadSeccompFilters()
	if err != nil {
		return err
	}
	program := unix.SockFprog{
		Len:    uint16(len(filters)),
		Filter: &filters[0],
	}
	return unix.Prctl(
		unix.PR_SET_SECCOMP,
		unix.SECCOMP_MODE_FILTER,
		uintptr(unsafe.Pointer(&program)),
		0,
		0,
	)
}

func workloadSeccompFilters() ([]unix.SockFilter, error) {
	namespaceCloneFlags := uint32(
		unix.CLONE_NEWNS |
			unix.CLONE_NEWCGROUP |
			unix.CLONE_NEWUTS |
			unix.CLONE_NEWIPC |
			unix.CLONE_NEWUSER |
			unix.CLONE_NEWPID |
			unix.CLONE_NEWNET,
	)
	filters := []unix.SockFilter{
		bpfStatement(unix.BPF_LD|unix.BPF_W|unix.BPF_ABS, seccompDataArchOffset),
		bpfJump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, auditArchX86_64, 1, 0),
		bpfStatement(unix.BPF_RET|unix.BPF_K, unix.SECCOMP_RET_KILL_PROCESS),
		bpfStatement(unix.BPF_LD|unix.BPF_W|unix.BPF_ABS, seccompDataNumberOffset),
		// The x32 ABI shares AUDIT_ARCH_X86_64 but uses syscall numbers with
		// bit 30 set. Reject it so it cannot bypass the native syscall rules.
		bpfJump(unix.BPF_JMP|unix.BPF_JGE|unix.BPF_K, x32SyscallBit, 0, 1),
		bpfStatement(unix.BPF_RET|unix.BPF_K, unix.SECCOMP_RET_ERRNO|uint32(unix.ENOSYS)),
		// clone3 stores its flags behind a pointer that classic seccomp cannot
		// inspect. Return ENOSYS so libc and language runtimes use clone.
		bpfJump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, unix.SYS_CLONE3, 0, 1),
		bpfStatement(unix.BPF_RET|unix.BPF_K, unix.SECCOMP_RET_ERRNO|uint32(unix.ENOSYS)),
		// Preserve ordinary clone-based threads and processes, but reject every
		// namespace-creating flag. Reload the syscall number for later rules.
		bpfJump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, unix.SYS_CLONE, 0, 3),
		bpfStatement(unix.BPF_LD|unix.BPF_W|unix.BPF_ABS, seccompDataArg0Offset),
		bpfJump(unix.BPF_JMP|unix.BPF_JSET|unix.BPF_K, namespaceCloneFlags, 0, 1),
		bpfStatement(unix.BPF_RET|unix.BPF_K, unix.SECCOMP_RET_ERRNO|uint32(unix.EPERM)),
		bpfStatement(unix.BPF_LD|unix.BPF_W|unix.BPF_ABS, seccompDataNumberOffset),
	}
	for _, syscall := range deniedWorkloadSyscalls {
		filters = append(
			filters,
			bpfJump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, syscall, 0, 1),
			bpfStatement(
				unix.BPF_RET|unix.BPF_K,
				unix.SECCOMP_RET_ERRNO|uint32(unix.EPERM),
			),
		)
	}
	filters = append(filters, bpfStatement(unix.BPF_RET|unix.BPF_K, unix.SECCOMP_RET_ALLOW))
	if len(filters) > int(^uint16(0)) {
		return nil, errors.New("seccomp filter exceeds BPF instruction limit")
	}
	return filters, nil
}

func bpfStatement(code uint16, value uint32) unix.SockFilter {
	return unix.SockFilter{Code: code, K: value}
}

func bpfJump(code uint16, value uint32, jumpTrue, jumpFalse uint8) unix.SockFilter {
	return unix.SockFilter{
		Code: code,
		Jt:   jumpTrue,
		Jf:   jumpFalse,
		K:    value,
	}
}
