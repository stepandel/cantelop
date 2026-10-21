//go:build linux && amd64

package guestrunner

import (
	"reflect"
	"testing"

	"golang.org/x/sys/unix"
)

func TestRootWorkloadCapabilityContract(t *testing.T) {
	want := []int{
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
	if !reflect.DeepEqual(rootWorkloadCapabilities, want) {
		t.Fatalf("root workload capabilities = %v, want %v", rootWorkloadCapabilities, want)
	}

	mask := capabilityMask(rootWorkloadCapabilities)
	for _, prohibited := range []int{
		unix.CAP_NET_ADMIN,
		unix.CAP_NET_RAW,
		unix.CAP_SYS_ADMIN,
		unix.CAP_SYS_BOOT,
		unix.CAP_SYS_MODULE,
		unix.CAP_SYS_PTRACE,
		unix.CAP_SYS_RAWIO,
		unix.CAP_SYS_TIME,
		unix.CAP_MKNOD,
		unix.CAP_BPF,
		unix.CAP_PERFMON,
		// The container root is entered with chroot alone. A workload that can
		// call chroot again walks out of it through a directory it owns.
		unix.CAP_SYS_CHROOT,
	} {
		if capabilityAllowed(mask, prohibited) {
			t.Errorf("prohibited capability %d is allowed", prohibited)
		}
	}
}

// Confinement is chroot without pivot_root, so the chroot syscall itself is the
// escape. Capabilities already deny it; seccomp is the independent boundary
// that holds if that policy regresses.
func TestSeccompFilterDeniesChroot(t *testing.T) {
	for _, denied := range deniedWorkloadSyscalls {
		if denied == unix.SYS_CHROOT {
			return
		}
	}
	t.Fatal("chroot is not in the denied syscall set")
}

func TestSeccompFilterDeniesEveryPrivilegedSyscall(t *testing.T) {
	filters, err := workloadSeccompFilters()
	if err != nil {
		t.Fatalf("workloadSeccompFilters(): %v", err)
	}
	const syscallRulesOffset = 13
	if got, want := len(filters), syscallRulesOffset+1+2*len(deniedWorkloadSyscalls); got != want {
		t.Fatalf("filter length = %d, want %d", got, want)
	}
	if filters[2].K != unix.SECCOMP_RET_KILL_PROCESS {
		t.Errorf("architecture mismatch action = %#x, want kill", filters[2].K)
	}
	for index, syscall := range deniedWorkloadSyscalls {
		comparison := filters[syscallRulesOffset+2*index]
		denial := filters[syscallRulesOffset+1+2*index]
		if comparison.K != syscall || comparison.Jt != 0 || comparison.Jf != 1 {
			t.Errorf(
				"syscall filter %d = %#v, want comparison for syscall %d",
				index,
				comparison,
				syscall,
			)
		}
		if denial.K != unix.SECCOMP_RET_ERRNO|uint32(unix.EPERM) {
			t.Errorf("syscall %d action = %#x, want EPERM", syscall, denial.K)
		}
	}
	if got := filters[len(filters)-1].K; got != unix.SECCOMP_RET_ALLOW {
		t.Errorf("default seccomp action = %#x, want allow", got)
	}
}
