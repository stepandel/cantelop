package hetzner_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stepandel/cantelop/fire-fuse/config"
	"github.com/stepandel/cantelop/fire-fuse/guest-runtime/guestconfig"
)

func TestDockerMaterializerPreservesDockerExecutionMetadata(t *testing.T) {
	script, err := os.ReadFile("prepare-docker-image.sh")
	if err != nil {
		t.Fatalf("read Docker image materializer: %v", err)
	}
	contents := string(script)
	for _, want := range []string{
		"docker image inspect",
		".Config.Entrypoint",
		".Config.Cmd",
		".Config.Env",
		".Config.WorkingDir",
		".Config.User",
		"docker export",
		"mkfs.ext4",
		`output_image_checksum="${output_image}.sha256"`,
		`output_process_checksum="${output_process}.sha256"`,
		"output paths must be distinct",
		`mv -nT -- "${source_path}" "${destination_path}"`,
		"output appeared during publication",
		`publish "${staging_image_checksum}" "${output_image_checksum}"`,
		`publish "${staging_process_checksum}" "${output_process_checksum}"`,
	} {
		if !strings.Contains(contents, want) {
			t.Errorf("Docker image materializer does not contain %q", want)
		}
	}
}

func TestCatalogBuilderMatchesRuntimeImageIdentity(t *testing.T) {
	rootfs := []byte("content-addressed-rootfs")
	process := guestconfig.Process{
		Args:             []string{"/bin/server", "<&>\u2028"},
		Env:              []string{"PATH=/usr/bin:/bin"},
		WorkingDirectory: "/app",
		UID:              1000,
		GID:              1000,
	}
	processDocument, err := json.Marshal(map[string]any{
		"gid": float64(process.GID), "uid": float64(process.UID),
		"working_directory": process.WorkingDirectory,
		"env":               process.Env,
		"args":              process.Args,
	})
	if err != nil {
		t.Fatal(err)
	}
	processPath := filepath.Join(t.TempDir(), "application.process.json")
	if err := os.WriteFile(processPath, processDocument, 0o600); err != nil {
		t.Fatal(err)
	}
	rootfsDigest := sha256.Sum256(rootfs)
	command := exec.Command(
		"python3",
		"../ansible/roles/compute/fire_fuse_stage/files/build-image-catalog.py",
		"--rootfs-size-bytes", fmt.Sprint(len(rootfs)),
		"--rootfs-sha256", fmt.Sprintf("%x", rootfsDigest),
		"--process", processPath,
		"--installed-rootfs", "/var/lib/fire-fuse/images/application.ext4",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("build image catalog: %v: %s", err, output)
	}
	var result struct {
		BuildID string                `json:"build_id"`
		Image   config.ContainerImage `json:"image"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("decode catalog output: %v", err)
	}
	wantID, err := config.ContainerImageID(result.Image)
	if err != nil {
		t.Fatalf("ContainerImageID() error = %v", err)
	}
	if result.BuildID != wantID {
		t.Fatalf("catalog Build ID = %q, runtime = %q", result.BuildID, wantID)
	}
}

func TestGuestBuilderMarksTheContainerFixture(t *testing.T) {
	script, err := os.ReadFile("prepare-guest.sh")
	if err != nil {
		t.Fatalf("read guest builder: %v", err)
	}
	contents := string(script)
	for _, want := range []string{
		"fire-fuse-container-rootfs-id",
		"fire-fuse-container-rootfs-v1",
		`ssh-keygen -q -A -f "${work_directory}/rootfs"`,
		"ssh_host_ed25519_key",
		"guest SSH Ed25519 private host key has an unsafe mode",
		`rm -f "${work_directory}/rootfs/etc/ssh/ssh_host_"*`,
		"bun-linux-x64-baseline.zip",
		"41201a8c5ee74a9dcbb1ce25a1104f1f929838b57a845aa78d98379b0ce7cde2",
		`rootfs/usr/local/lib/cantelop/bun`,
		`install -d -m 0755 "${work_directory}/rootfs/run/sshd"`,
		`"${work_directory}/container-ubuntu-24.ext4"`,
	} {
		if !strings.Contains(contents, want) {
			t.Errorf("guest builder does not contain %q", want)
		}
	}
}

func TestGuestBuilderRequiresContainerOverlayKernelSupport(t *testing.T) {
	script, err := os.ReadFile("prepare-guest.sh")
	if err != nil {
		t.Fatalf("read guest builder: %v", err)
	}
	contents := string(script)
	for _, option := range []string{"OVERLAY_FS", "TMPFS"} {
		if !strings.Contains(contents, "\n    "+option+"\n") {
			t.Errorf("guest builder does not require CONFIG_%s", option)
		}
	}
	for _, want := range []string{
		"kernel_required_runtime_options=(",
		`"${kernel_required_runtime_options[@]}"`,
		"rebuilt guest kernel does not build in CONFIG_${option}",
	} {
		if !strings.Contains(contents, want) {
			t.Errorf("guest builder does not contain %q", want)
		}
	}
}

func TestGuestBuilderDisablesPrivilegedKernelFacilities(t *testing.T) {
	script, err := os.ReadFile("prepare-guest.sh")
	if err != nil {
		t.Fatalf("read guest builder: %v", err)
	}
	contents := string(script)
	for _, option := range []string{
		"MODULES",
		"KEXEC",
		"KEXEC_FILE",
		"KEXEC_JUMP",
		"CRASH_DUMP",
		"LIVEPATCH",
		"KVM",
		"KVM_INTEL",
		"KVM_AMD",
		"VHOST_MENU",
		"VHOST_NET",
		"VHOST_SCSI",
		"VHOST_VSOCK",
		"VHOST_VDPA",
		"DEVMEM",
		"DEVKMEM",
		"DEVPORT",
		"PROC_KCORE",
		"DEBUG_FS",
	} {
		if !strings.Contains(contents, "\n    "+option+"\n") {
			t.Errorf("guest builder does not disable CONFIG_%s", option)
		}
	}
	for _, want := range []string{
		`scripts/config --disable "${option}"`,
		`scripts/config --state "${option}"`,
		"n | undef)",
		"rebuilt guest kernel unexpectedly enables CONFIG_${option}",
	} {
		if !strings.Contains(contents, want) {
			t.Errorf("guest builder does not contain %q", want)
		}
	}
}

func TestGuestBuilderRestrictsHardwareToFirecrackerDevices(t *testing.T) {
	script, err := os.ReadFile("prepare-guest.sh")
	if err != nil {
		t.Fatalf("read guest builder: %v", err)
	}
	contents := string(script)
	for _, option := range []string{
		"INPUT_EVDEV",
		"INPUT_JOYDEV",
		"INPUT_MOUSEDEV",
		"INPUT_MOUSE",
		"INPUT_JOYSTICK",
		"INPUT_TABLET",
		"INPUT_TOUCHSCREEN",
		"INPUT_MISC",
		"GAMEPORT",
		"HID_SUPPORT",
		"USB_SUPPORT",
		"SOUND",
		"MEDIA_SUPPORT",
		"DRM",
		"FB",
		"AGP",
		"SCSI",
		"ATA",
		"BLK_DEV_NVME",
		"MMC",
		"MEMSTICK",
		"MTD",
		"FIREWIRE",
		"ETHERNET",
		"WLAN",
		"WWAN",
		"BT",
		"RFKILL",
		"NFC",
		"CAN",
		"HAMRADIO",
		"INFINIBAND",
		"ISDN",
		"PARPORT",
		"PCCARD",
	} {
		if !strings.Contains(contents, "\n    "+option+"\n") {
			t.Errorf("guest builder does not disable CONFIG_%s", option)
		}
	}
	for _, option := range []string{
		"HYPERVISOR_GUEST",
		"KVM_GUEST",
		"VIRTIO",
		"VIRTIO_MMIO",
		"VIRTIO_MMIO_CMDLINE_DEVICES",
		"VIRTIO_BLK",
		"VIRTIO_NET",
		"SERIAL_8250",
		"SERIAL_8250_CONSOLE",
		"INPUT",
		"INPUT_KEYBOARD",
		"KEYBOARD_ATKBD",
		"SERIO",
		"SERIO_I8042",
		"SERIO_LIBPS2",
	} {
		if !strings.Contains(contents, "\n    "+option+"\n") {
			t.Errorf("guest builder does not require CONFIG_%s", option)
		}
	}
	for _, want := range []string{
		`scripts/config --enable "${option}"`,
		"rebuilt guest kernel does not build in CONFIG_${option}",
	} {
		if !strings.Contains(contents, want) {
			t.Errorf("guest builder does not contain %q", want)
		}
	}
}

func TestGuestBuilderPreservesApplicationNetworkingWithoutNetworkAdministration(t *testing.T) {
	script, err := os.ReadFile("prepare-guest.sh")
	if err != nil {
		t.Fatalf("read guest builder: %v", err)
	}
	contents := string(script)
	for _, option := range []string{
		"NET_NS",
		"PACKET",
		"NETFILTER",
		"NET_SCHED",
		"BRIDGE",
		"VLAN_8021Q",
		"OPENVSWITCH",
		"BONDING",
		"DUMMY",
		"NET_TEAM",
		"MACVLAN",
		"IPVLAN",
		"VXLAN",
		"GENEVE",
		"TUN",
		"VETH",
		"PPP",
		"SLIP",
		"MACSEC",
		"WIREGUARD",
		"L2TP",
		"PPTP",
		"NET_IPIP",
		"NET_IPGRE",
		"IPV6_SIT",
		"IPV6_TUNNEL",
		"IPV6_GRE",
		"NET_KEY",
		"XFRM_USER",
		"INET_AH",
		"INET_ESP",
		"INET_IPCOMP",
		"INET6_AH",
		"INET6_ESP",
		"INET6_IPCOMP",
		"IP_ADVANCED_ROUTER",
		"IP_MULTIPLE_TABLES",
		"IP_ROUTE_MULTIPATH",
		"IP_MROUTE",
		"IPV6_SEG6_LWTUNNEL",
		"IPV6_SEG6_HMAC",
		"IPV6_MULTIPLE_TABLES",
		"IP_SCTP",
		"DCCP",
		"RDS",
		"TIPC",
		"ATM",
		"X25",
		"LAPB",
		"PHONET",
		"IEEE802154",
		"6LOWPAN",
		"AF_RXRPC",
		"MPLS",
		"NET_NSH",
	} {
		if !strings.Contains(contents, "\n    "+option+"\n") {
			t.Errorf("guest builder does not disable CONFIG_%s", option)
		}
	}
	for _, option := range []string{
		"NET",
		"UNIX",
		"INET",
		"IPV6",
		"IP_PNP",
		"SUNRPC",
	} {
		if !strings.Contains(contents, "\n    "+option+"\n") {
			t.Errorf("guest builder does not require CONFIG_%s", option)
		}
	}
	for _, want := range []string{
		"kernel_required_network_options=(",
		`"${kernel_required_network_options[@]}"`,
	} {
		if !strings.Contains(contents, want) {
			t.Errorf("guest builder does not contain %q", want)
		}
	}
}

func TestGuestBuilderRequiresWorkloadSeccomp(t *testing.T) {
	script, err := os.ReadFile("prepare-guest.sh")
	if err != nil {
		t.Fatalf("read guest builder: %v", err)
	}
	contents := string(script)
	for _, option := range []string{"SECCOMP", "SECCOMP_FILTER"} {
		if !strings.Contains(contents, "\n    "+option+"\n") {
			t.Errorf("guest builder does not require CONFIG_%s", option)
		}
	}
	for _, want := range []string{
		"kernel_required_workload_security_options=(",
		`"${kernel_required_workload_security_options[@]}"`,
		"rebuilt guest kernel does not build in CONFIG_${option}",
	} {
		if !strings.Contains(contents, want) {
			t.Errorf("guest builder does not contain %q", want)
		}
	}
}

func TestHostConfigurationDefaultDeniesGuestEgress(t *testing.T) {
	for _, path := range []string{"../ansible/roles/compute/fire_fuse_stage/templates/config.json.j2"} {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, want := range []string{
			`"ip6tables_bin": "/usr/sbin/ip6tables"`,
			`"dns_resolvers": ["1.1.1.1", "8.8.8.8"]`,
			`"allowed_tcp_ports": [22, 80, 443]`,
			`"allowed_udp_ports": [443]`,
		} {
			if !strings.Contains(string(contents), want) {
				t.Errorf("%s does not contain %q", path, want)
			}
		}
	}
}

func TestGuestBuilderTrimsUnusedKernelSubsystems(t *testing.T) {
	script, err := os.ReadFile("prepare-guest.sh")
	if err != nil {
		t.Fatalf("read guest builder: %v", err)
	}
	contents := string(script)
	for _, option := range []string{
		"EXT2_FS",
		"XFS_FS",
		"BTRFS_FS",
		"FUSE_FS",
		"SQUASHFS",
		"VFAT_FS",
		"NTFS3_FS",
		"ISO9660_FS",
		"CIFS",
		"CEPH_FS",
		"9P_FS",
		"NFSD",
		"NFS_V3",
		"BINFMT_MISC",
		"IA32_EMULATION",
		"X86_X32",
		"BPF_SYSCALL",
		"KPROBES",
		"HW_PERF_EVENTS",
		"FTRACE",
		"USER_NS",
		"CHECKPOINT_RESTORE",
		"HOTPLUG_PCI",
		"EFI",
		"SUSPEND",
		"BOOTPARAM_HOTPLUG_CPU0",
		"NUMA",
		"SCHED_MC_PRIO",
		"CPU_FREQ",
		"ACPI_PROCESSOR",
		"CPU_IDLE",
	} {
		if !strings.Contains(contents, "\n    "+option+"\n") {
			t.Errorf("guest builder does not disable CONFIG_%s", option)
		}
	}
	if !strings.Contains(contents, "seccomp policy denies it") {
		t.Error("guest builder does not explain the unavoidable PERF_EVENTS core")
	}
	if !strings.Contains(contents, "HOTPLUG_CPU core whenever SMP is built") {
		t.Error("guest builder does not explain the unavoidable HOTPLUG_CPU core")
	}
	if !strings.Contains(contents, "supported virtio PCI transport") {
		t.Error("guest builder does not explain why the PCI core remains")
	}
	for _, option := range []string{"MEMORY_HOTPLUG", "MEMORY_HOTREMOVE", "VIRTIO_MEM"} {
		if !strings.Contains(contents, "    "+option+"\n") {
			t.Errorf("guest builder does not enable CONFIG_%s", option)
		}
	}
	for _, option := range []string{
		"BINFMT_ELF",
		"BINFMT_SCRIPT",
		"CGROUPS",
		"NAMESPACES",
		"PROC_FS",
		"SYSFS",
		"DEVTMPFS",
		"UNIX98_PTYS",
		"EXT4_FS",
		"OVERLAY_FS",
		"TMPFS",
		"NFS_FS",
		"NFS_V4_2",
		"VIRTIO_PCI",
		"PCI_MSI",
	} {
		if !strings.Contains(contents, "\n    "+option+"\n") {
			t.Errorf("guest builder does not require CONFIG_%s", option)
		}
	}
}

func TestGuestBuilderEmbedsPlatformOverlayInit(t *testing.T) {
	script, err := os.ReadFile("prepare-guest.sh")
	if err != nil {
		t.Fatalf("read guest builder: %v", err)
	}
	contents := string(script)
	for _, want := range []string{
		"./fire-fuse/guest-runtime/cmd/fire-fuse-init",
		`"${kernel_source_directory}/fire-fuse-initramfs/init"`,
		`"${kernel_source_directory}/fire-fuse-initramfs/dev/console" c 5 1`,
		"kernel_required_runtime_options=(",
		`"${kernel_required_runtime_options[@]}"`,
		"--set-str INITRAMFS_SOURCE fire-fuse-initramfs",
		`CONFIG_INITRAMFS_SOURCE="fire-fuse-initramfs"`,
		"does not embed fire-fuse init",
	} {
		if !strings.Contains(contents, want) {
			t.Errorf("guest builder does not contain %q", want)
		}
	}
}

func TestGuestBuilderPublishesArtifactChecksums(t *testing.T) {
	script, err := os.ReadFile("prepare-guest.sh")
	if err != nil {
		t.Fatalf("read guest builder: %v", err)
	}
	contents := string(script)
	for _, artifact := range []string{
		"vmlinux",
		"ubuntu-24.ext4",
		"container-ubuntu-24.ext4",
		"container-ubuntu-24.process.json",
	} {
		selfContainedChecksum := "sha256sum " + artifact +
			" > " + artifact + ".sha256"
		if !strings.Contains(contents, selfContainedChecksum) {
			t.Errorf(
				"guest builder does not publish checksum command %q",
				selfContainedChecksum,
			)
		}
	}
	for _, want := range []string{
		"umask 0022",
		"publication_complete=0",
		`mv -nT -- "${source_path}" "${destination_path}"`,
		"output appeared during publication",
		`mktemp "${output_directory}/.${release_name}.tmp.XXXXXX"`,
		`rm -f -- "${published_path}"`,
		"publication_complete=1",
	} {
		if !strings.Contains(contents, want) {
			t.Errorf("guest builder publication does not contain %q", want)
		}
	}
}

func TestGuestFixturePublishesAValidProcessContract(t *testing.T) {
	document, err := os.ReadFile("guest/container-ubuntu-24.process.json")
	if err != nil {
		t.Fatalf("read guest fixture process contract: %v", err)
	}
	var process guestconfig.Process
	if err := json.Unmarshal(document, &process); err != nil {
		t.Fatalf("decode guest fixture process contract: %v", err)
	}
	config := guestconfig.Config{
		SchemaVersion: guestconfig.SchemaVersion,
		Process:       process,
	}
	if err := config.Validate(); err != nil {
		t.Fatalf("validate guest fixture process contract: %v", err)
	}
	wantArgs := []string{
		"/bin/sleep",
		"3600",
	}
	if got := process.Args; !reflect.DeepEqual(got, wantArgs) {
		t.Fatalf("guest fixture workload args = %q, want %q", got, wantArgs)
	}
	if process.UID != 65534 || process.GID != 65534 {
		t.Fatalf(
			"guest fixture identity = %d:%d, want unprivileged 65534:65534",
			process.UID,
			process.GID,
		)
	}
}

func TestContainerProcessValidator(t *testing.T) {
	valid, err := os.ReadFile("guest/container-ubuntu-24.process.json")
	if err != nil {
		t.Fatalf("read valid process fixture: %v", err)
	}
	var process map[string]any
	if err := json.Unmarshal(valid, &process); err != nil {
		t.Fatalf("decode valid process fixture: %v", err)
	}
	withWorkingDirectory := func(value string) []byte {
		t.Helper()
		changed := make(map[string]any, len(process))
		for key, field := range process {
			changed[key] = field
		}
		changed["working_directory"] = value
		document, err := json.Marshal(changed)
		if err != nil {
			t.Fatalf("encode changed process fixture: %v", err)
		}
		return document
	}
	withEnvironment := func(value string) []byte {
		t.Helper()
		changed := make(map[string]any, len(process))
		for key, field := range process {
			changed[key] = field
		}
		changed["env"] = []string{value}
		document, err := json.Marshal(changed)
		if err != nil {
			t.Fatalf("encode changed process fixture: %v", err)
		}
		return document
	}

	tests := []struct {
		name     string
		document []byte
		wantOK   bool
	}{
		{name: "valid", document: valid, wantOK: true},
		{
			name: "integral numeric identities",
			document: []byte(
				`{"args":["/bin/true"],"env":[],"gid":2.0,` +
					`"uid":1e2,"working_directory":"/"}`,
			),
			wantOK: true,
		},
		{name: "multiple documents", document: append(append([]byte{}, valid...), valid...)},
		{name: "parent traversal", document: withWorkingDirectory("/app/../tmp")},
		{name: "NUL", document: withWorkingDirectory("/app\x00tmp")},
		{
			name:     "UTF-8 byte limit",
			document: withEnvironment("VALUE=" + strings.Repeat("😀", 20_000)),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "process.json")
			if err := os.WriteFile(path, test.document, 0o600); err != nil {
				t.Fatalf("write process fixture: %v", err)
			}
			command := exec.Command(
				"jq",
				"-e",
				"-s",
				"-f",
				"validate-container-process.jq",
				path,
			)
			err := command.Run()
			if test.wantOK && err != nil {
				t.Fatalf("validator rejected valid process contract: %v", err)
			}
			if !test.wantOK && err == nil {
				t.Fatal("validator accepted invalid process contract")
			}
		})
	}
}
