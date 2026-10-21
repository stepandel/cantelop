package guest_test

import (
	"bufio"
	"os"
	"strings"
	"testing"
)

func TestMemoryStatusProbeStaysOnPrivateGuestInterface(t *testing.T) {
	socket := readUnit(t, "fire-fuse-memory-status.socket")
	assertUnitValue(t, socket, "Socket.ListenStream", "0.0.0.0:19095")
	assertUnitValue(t, socket, "Socket.BindToDevice", "eth0")
	assertUnitValue(t, socket, "Socket.Accept", "yes")
	service := readUnit(t, "fire-fuse-memory-status@.service")
	assertUnitValue(t, service, "Service.StandardOutput", "socket")
	assertUnitValue(t, service, "Service.ExecStart", "/usr/local/sbin/fire-fuse-memory-status")
}

func TestSnapshotBootTargetWaitsForAssignmentAndQualificationSSH(t *testing.T) {
	target := readUnit(t, "fire-fuse-snapshot.target")
	assertUnitValues(
		t,
		target,
		"Unit.Requires",
		[]string{
			"fire-fuse-assignment-agent.service",
			"fire-fuse-platform-runtime-prime.service",
			"fire-fuse-memory-status.socket",
		},
	)
	assertUnitValues(
		t,
		target,
		"Unit.After",
		[]string{
			"fire-fuse-assignment-agent.service",
			"fire-fuse-platform-runtime-prime.service",
			"ssh.service",
		},
	)
	assertUnitValue(t, target, "Unit.Wants", "ssh.service")
	assertUnitValue(t, target, "Unit.AllowIsolate", "yes")
	for _, forbidden := range []string{
		"fire-fuse-assigned-workload.service",
		"multi-user.target",
		"graphical.target",
	} {
		for _, dependency := range append(
			append([]string{}, target["Unit.Requires"]...),
			target["Unit.Wants"]...,
		) {
			if dependency == forbidden {
				t.Errorf("snapshot target starts %s before assignment", forbidden)
			}
		}
	}

	agent := readUnit(t, "fire-fuse-assignment-agent.service")
	assertUnitValue(t, agent, "Unit.Requires", "dev-vdb.device")
	assertUnitValue(t, agent, "Unit.After", "dev-vdb.device")
	assertUnitValue(
		t,
		agent,
		"Service.ExecStart",
		"/usr/local/sbin/fire-fuse-assignment-agent",
	)
	assertUnitValue(t, agent, "Service.Restart", "no")

	runtimePrime := readUnit(t, "fire-fuse-platform-runtime-prime.service")
	assertUnitValue(
		t,
		runtimePrime,
		"Service.ExecStart",
		"/usr/local/sbin/fire-fuse-prime-platform-runtime",
	)
	assertUnitValue(t, runtimePrime, "Service.Type", "oneshot")

	workload := readUnit(t, "fire-fuse-assigned-workload.service")
	assertUnitValue(
		t,
		workload,
		"Service.ExecStart",
		"/usr/local/sbin/fire-fuse-guest",
	)
	assertUnitValues(t, workload, "Service.ExecStopPost", []string{
		"-/usr/local/sbin/fire-fuse-record-workload-exit",
		"/usr/bin/systemctl --no-block poweroff",
	})
	if values := workload["Install.WantedBy"]; len(values) != 0 {
		t.Errorf("assigned workload is enabled before assignment: %q", values)
	}
}

func TestWorkspaceMountUsesKernelNFSClient(t *testing.T) {
	script, err := os.ReadFile("fire-fuse-mount-workspace")
	if err != nil {
		t.Fatalf("read workspace mount script: %v", err)
	}
	contents := string(script)
	for _, want := range []string{
		"read_value client-id",
		"/sys/fs/nfs/net/nfs_client/identifier",
		"/usr/sbin/mount.nfs4",
		"vers=4.2,proto=tcp,port=2049,hard,timeo=600,retrans=2",
	} {
		if !strings.Contains(contents, want) {
			t.Errorf("workspace mount script does not contain %q", want)
		}
	}
	if strings.Index(contents, "printf '%s\\n' \"${client_id}\" > \"${nfs_client_identifier}\"") >
		strings.Index(contents, "\n/usr/sbin/mount.nfs4") {
		t.Error("NFSv4 client identifier must be set before the first mount")
	}
	for _, forbidden := range []string{
		"/usr/bin/mount",
		"--internal-only",
		"juicefs",
		"meta-url",
		"cache-size-mib",
	} {
		if strings.Contains(contents, forbidden) {
			t.Errorf("workspace mount script contains obsolete guest JuiceFS value %q", forbidden)
		}
	}
}

func TestContainerRootFSMountUsesReadOnlyBaseAndEphemeralUpper(t *testing.T) {
	script, err := os.ReadFile("fire-fuse-mount-container-rootfs")
	if err != nil {
		t.Fatalf("read container rootfs mount script: %v", err)
	}
	contents := string(script)
	for _, want := range []string{
		"readonly device='/dev/vdb'",
		"readonly merged='/run/fire-fuse/container-rootfs'",
		"readonly overlay_runtime='/run/fire-fuse/container-overlay'",
		"readonly upper_size='128m'",
		"--types tmpfs",
		"--types ext4",
		"--options ro,nodev,nosuid",
		"--types overlay",
		"lowerdir=${lower},upperdir=${upper},workdir=${work}",
		"fire-fuse-unmount-container-rootfs",
	} {
		if !strings.Contains(contents, want) {
			t.Errorf("container rootfs mount script does not contain %q", want)
		}
	}
}

func TestAssignedContainerDeviceIsFlushedBeforeMount(t *testing.T) {
	script, err := os.ReadFile("fire-fuse-prepare-container-device")
	if err != nil {
		t.Fatalf("read container device preparation script: %v", err)
	}
	contents := string(script)
	for _, want := range []string{
		"readonly device='/dev/vdb'",
		"done < /proc/self/mounts",
		"/usr/sbin/blockdev --flushbufs",
		"/usr/sbin/blockdev --getsize64",
	} {
		if !strings.Contains(contents, want) {
			t.Errorf("container device preparation script does not contain %q", want)
		}
	}
}

func TestContainerRootFSUnmountsOverlayInDependencyOrder(t *testing.T) {
	script, err := os.ReadFile("fire-fuse-unmount-container-rootfs")
	if err != nil {
		t.Fatalf("read container rootfs unmount script: %v", err)
	}
	contents := string(script)
	for _, want := range []string{
		`for mountpoint in "${merged}" "${lower}" "${overlay_runtime}"`,
		"/usr/bin/mountpoint --quiet",
		`/usr/bin/umount "${mountpoint}"`,
	} {
		if !strings.Contains(contents, want) {
			t.Errorf("container rootfs unmount script does not contain %q", want)
		}
	}
}

func TestGuestPreparationInstallsWorkloadRuntime(t *testing.T) {
	script, err := os.ReadFile("../prepare-guest.sh")
	if err != nil {
		t.Fatalf("read guest preparation script: %v", err)
	}
	contents := string(script)
	for _, want := range []string{
		"CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build",
		"./fire-fuse/guest-runtime/cmd/fire-fuse-guest",
		"./fire-fuse/guest-runtime/cmd/fire-fuse-assignment-agent",
		"/usr/local/sbin/fire-fuse-guest",
		"/usr/local/sbin/fire-fuse-assignment-agent",
		"fire-fuse-record-workload-exit",
		"fire-fuse-prepare-container-device",
		"fire-fuse-mount-container-rootfs",
		"fire-fuse-unmount-container-rootfs",
		"fire-fuse-wait-workspace-network",
		"fire-fuse-mount-workspace",
		"fire-fuse-wait-workspace",
		"fire-fuse-assignment-agent.service",
		"fire-fuse-assigned-workload.service",
		"fire-fuse-platform-runtime-prime.service",
		"fire-fuse-snapshot.target",
		"fire-fuse-memory-status.socket",
		"fire-fuse-memory-status@.service",
		"fire-fuse-memory-status",
		"VIRTIO_VSOCKETS",
	} {
		if !strings.Contains(contents, want) {
			t.Errorf("guest preparation script does not contain %q", want)
		}
	}
}

func assertUnitValue(t *testing.T, unit map[string][]string, key, want string) {
	t.Helper()
	values := unit[key]
	if len(values) != 1 || values[0] != want {
		t.Errorf("%s = %q, want [%q]", key, values, want)
	}
}

func assertUnitValues(t *testing.T, unit map[string][]string, key string, want []string) {
	t.Helper()
	values := unit[key]
	if len(values) != len(want) {
		t.Errorf("%s = %q, want %q", key, values, want)
		return
	}
	for index := range want {
		if values[index] != want[index] {
			t.Errorf("%s[%d] = %q, want %q", key, index, values[index], want[index])
		}
	}
}

func readUnit(t *testing.T, path string) map[string][]string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer file.Close()

	values := make(map[string][]string)
	section := ""
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSuffix(strings.TrimPrefix(line, "["), "]")
			continue
		}
		name, value, found := strings.Cut(line, "=")
		if !found || section == "" || strings.TrimSpace(name) == "" {
			t.Fatalf("invalid unit line %q", line)
		}
		key := section + "." + strings.TrimSpace(name)
		values[key] = append(values[key], strings.TrimSpace(value))
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return values
}
