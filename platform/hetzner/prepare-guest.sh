#!/usr/bin/env bash
set -Eeuo pipefail
umask 0022

fail() {
  printf 'prepare-guest: %s\n' "$*" >&2
  exit 1
}

[[ "${EUID}" -eq 0 ]] || fail 'must run as root'
[[ "$(uname -s)" == 'Linux' ]] || fail 'requires Linux'
[[ $# -eq 2 ]] || fail 'usage: prepare-guest.sh PUBLIC_KEY_PATH OUTPUT_DIRECTORY'

script_directory="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repository_directory="$(cd "${script_directory}/../.." && pwd)"
public_key_path="$1"
output_directory="$2"
[[ -f "${public_key_path}" && -s "${public_key_path}" ]] || \
  fail "public key is not a non-empty regular file: ${public_key_path}"
[[ ! -e "${output_directory}/vmlinux" ]] || fail "output already exists: ${output_directory}/vmlinux"
[[ ! -e "${output_directory}/ubuntu-24.ext4" ]] || \
  fail "output already exists: ${output_directory}/ubuntu-24.ext4"
[[ ! -e "${output_directory}/container-ubuntu-24.ext4" ]] || \
  fail "output already exists: ${output_directory}/container-ubuntu-24.ext4"
[[ ! -e "${output_directory}/container-ubuntu-24.process.json" ]] || \
  fail "output already exists: ${output_directory}/container-ubuntu-24.process.json"
for checksum_name in \
  vmlinux ubuntu-24.ext4 container-ubuntu-24.ext4 \
  container-ubuntu-24.process.json
do
  [[ ! -e "${output_directory}/${checksum_name}.sha256" ]] || \
    fail "output already exists: ${output_directory}/${checksum_name}.sha256"
done

for command in \
  bison blockdev curl dpkg-deb flex gcc install ld make mkfs.ext4 nproc perl \
  python3 sha256sum ssh-keygen stat systemctl tar truncate unsquashfs
do
  command -v "${command}" >/dev/null 2>&1 || fail "required command is unavailable: ${command}"
done
prebuilt_guest_directory="${FIRE_FUSE_PREBUILT_GUEST_DIRECTORY:-}"
if [[ -z "${prebuilt_guest_directory}" ]]; then
  command -v go >/dev/null 2>&1 || fail 'required command is unavailable: go'
else
  [[ "${prebuilt_guest_directory}" == /* ]] || \
    fail 'FIRE_FUSE_PREBUILT_GUEST_DIRECTORY must be an absolute path'
  for guest_binary in fire-fuse-guest fire-fuse-assignment-agent fire-fuse-init; do
    [[ -f "${prebuilt_guest_directory}/${guest_binary}" && \
       -x "${prebuilt_guest_directory}/${guest_binary}" ]] || \
      fail "prebuilt guest binary is not an executable regular file: ${prebuilt_guest_directory}/${guest_binary}"
  done
fi
[[ -f "${script_directory}/guest/fire-fuse-mount-workspace" ]] || \
  fail 'guest workspace mount helper is missing'
[[ -f "${script_directory}/guest/fire-fuse-wait-workspace" ]] || \
  fail 'guest workspace readiness helper is missing'
[[ -f "${script_directory}/guest/fire-fuse-wait-workspace-network" ]] || \
  fail 'guest workspace network helper is missing'
[[ -f "${script_directory}/guest/fire-fuse-workspace-benchmark" ]] || \
  fail 'guest workspace benchmark helper is missing'
[[ -f "${script_directory}/guest/fire-fuse-mount-container-rootfs" ]] || \
  fail 'guest container rootfs mount helper is missing'
[[ -f "${script_directory}/guest/fire-fuse-unmount-container-rootfs" ]] || \
  fail 'guest container rootfs unmount helper is missing'
[[ -f "${script_directory}/guest/fire-fuse-prepare-container-device" ]] || \
  fail 'guest container rootfs preparation helper is missing'
[[ -f "${script_directory}/guest/fire-fuse-prime-platform-runtime" ]] || \
  fail 'guest platform runtime priming helper is missing'
[[ -f "${script_directory}/guest/fire-fuse-platform-runtime-prime.service" ]] || \
  fail 'guest platform runtime priming unit is missing'
[[ -f "${script_directory}/guest/fire-fuse-assignment-agent.service" ]] || \
  fail 'guest assignment agent systemd unit is missing'
[[ -f "${script_directory}/guest/fire-fuse-assigned-workload.service" ]] || \
  fail 'guest assigned workload systemd unit is missing'
[[ -f "${script_directory}/guest/fire-fuse-snapshot.target" ]] || \
  fail 'guest snapshot boot target is missing'
[[ -f "${script_directory}/guest/container-ubuntu-24.process.json" ]] || \
  fail 'guest fixture process configuration is missing'

kernel_url='https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.15/x86_64/vmlinux-6.1.155'
kernel_sha256='e20e46d0c36c55c0d1014eb20576171b3f3d922260d9f792017aeff53af3d4f2'
kernel_source_url='https://cdn.kernel.org/pub/linux/kernel/v6.x/linux-6.1.155.tar.xz'
kernel_source_sha256='c29387aeee085fbcbd91236224b9df805063bac43615e75cea2c6b29604a5c73'
rootfs_url='https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.15/x86_64/ubuntu-24.04.squashfs'
rootfs_sha256='68321e0482baeb3844dafe8a6b08a6902401a7afc41fbfd8c3d9ea08aadd244f'
nfs_common_url='https://archive.ubuntu.com/ubuntu/pool/main/n/nfs-utils/nfs-common_2.6.4-3ubuntu5.1_amd64.deb'
nfs_common_sha256='c1224e6d3eb7beaa55e765d7288b4f9715a343e11f7ccea9cc1eb0dc33cf82cf'
# Must match console/package.json packageManager (scripts/tests/test_bun_version_pins.py).
bun_url='https://github.com/oven-sh/bun/releases/download/bun-v1.3.10/bun-linux-x64-baseline.zip'
bun_sha256='41201a8c5ee74a9dcbb1ce25a1104f1f929838b57a845aa78d98379b0ce7cde2'

work_directory="$(mktemp -d)"
publication_complete=0
published_paths=()
publication_staging_paths=()
pending_source=''
pending_destination=''
cleanup() {
  for publication_staging_path in "${publication_staging_paths[@]}"; do
    rm -f -- "${publication_staging_path}"
  done
  if [[ "${publication_complete}" != '1' ]]; then
    if [[ -n "${pending_destination}" && ! -e "${pending_source}" ]]; then
      rm -f -- "${pending_destination}"
    fi
    for published_path in "${published_paths[@]}"; do
      rm -f -- "${published_path}"
    done
  fi
  [[ -n "${work_directory:-}" && "${work_directory}" == /tmp/* ]] || return
  rm -rf -- "${work_directory}"
}
trap cleanup EXIT

if [[ -n "${prebuilt_guest_directory}" ]]; then
  install -m 0755 "${prebuilt_guest_directory}/fire-fuse-guest" \
    "${work_directory}/fire-fuse-guest"
  install -m 0755 "${prebuilt_guest_directory}/fire-fuse-assignment-agent" \
    "${work_directory}/fire-fuse-assignment-agent"
  install -m 0755 "${prebuilt_guest_directory}/fire-fuse-init" \
    "${work_directory}/fire-fuse-init"
else
  (
    cd "${repository_directory}"
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
      -trimpath \
      -buildvcs=false \
      -ldflags='-s -w' \
      -o "${work_directory}/fire-fuse-guest" \
      ./fire-fuse/guest-runtime/cmd/fire-fuse-guest
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
      -trimpath \
      -buildvcs=false \
      -ldflags='-s -w' \
      -o "${work_directory}/fire-fuse-assignment-agent" \
      ./fire-fuse/guest-runtime/cmd/fire-fuse-assignment-agent
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
      -trimpath \
      -buildvcs=false \
      -ldflags='-s -w' \
      -o "${work_directory}/fire-fuse-init" \
      ./fire-fuse/guest-runtime/cmd/fire-fuse-init
  )
fi

curl --fail --location --silent --show-error \
  --output "${work_directory}/vmlinux" "${kernel_url}"
curl --fail --location --silent --show-error \
  --output "${work_directory}/linux.tar.xz" "${kernel_source_url}"
curl --fail --location --silent --show-error \
  --output "${work_directory}/ubuntu.squashfs" "${rootfs_url}"
curl --fail --location --silent --show-error \
  --output "${work_directory}/nfs-common.deb" "${nfs_common_url}"
curl --fail --location --silent --show-error \
  --output "${work_directory}/bun.zip" "${bun_url}"
printf '%s  %s\n' "${bun_sha256}" "${work_directory}/bun.zip" | sha256sum --check --status - || \
  fail 'downloaded Bun runtime checksum does not match the pinned release asset'
python3 - "${work_directory}/bun.zip" "${work_directory}/bun-release" <<'PY'
import pathlib
import sys
import zipfile

archive = pathlib.Path(sys.argv[1])
destination = pathlib.Path(sys.argv[2])
destination.mkdir(mode=0o755)
with zipfile.ZipFile(archive) as bundle:
    expected = "bun-linux-x64-baseline/bun"
    names = bundle.namelist()
    if expected not in names:
        raise SystemExit("pinned Bun archive does not contain the expected executable")
    member = bundle.getinfo(expected)
    if member.is_dir() or member.file_size <= 0:
        raise SystemExit("pinned Bun executable member is invalid")
    target = destination / expected
    target.parent.mkdir(parents=True, mode=0o755)
    with bundle.open(member) as source, target.open("xb") as output:
        while chunk := source.read(1024 * 1024):
            output.write(chunk)
    target.chmod(0o755)
PY
[[ -x "${work_directory}/bun-release/bun-linux-x64-baseline/bun" ]] || \
  fail 'downloaded Bun runtime archive does not contain the expected executable'

verify_sha256() {
  local path="$1"
  local expected="$2"
  local actual
  actual="$(sha256sum "${path}")"
  actual="${actual%% *}"
  [[ "${actual}" == "${expected}" ]] || fail "SHA-256 mismatch for ${path}"
}
verify_sha256 "${work_directory}/vmlinux" "${kernel_sha256}"
verify_sha256 "${work_directory}/linux.tar.xz" "${kernel_source_sha256}"
verify_sha256 "${work_directory}/ubuntu.squashfs" "${rootfs_sha256}"
verify_sha256 "${work_directory}/nfs-common.deb" "${nfs_common_sha256}"

tar -xf "${work_directory}/linux.tar.xz" -C "${work_directory}"
kernel_source_directory="${work_directory}/linux-6.1.155"
install -d -m 0755 "${kernel_source_directory}/fire-fuse-initramfs"
install -m 0755 \
  "${work_directory}/fire-fuse-init" \
  "${kernel_source_directory}/fire-fuse-initramfs/init"
install -d -m 0755 "${kernel_source_directory}/fire-fuse-initramfs/dev"
mknod -m 0600 \
  "${kernel_source_directory}/fire-fuse-initramfs/dev/console" c 5 1
"${kernel_source_directory}/scripts/extract-ikconfig" \
  "${work_directory}/vmlinux" > "${kernel_source_directory}/.config"
(
  cd "${kernel_source_directory}"
  # The workload must not extend or replace its kernel, act as a hypervisor or
  # vhost backend, or access privileged kernel-memory debugging interfaces.
  kernel_disabled_options=(
    MODULES
    KEXEC
    KEXEC_FILE
    KEXEC_JUMP
    CRASH_DUMP
    LIVEPATCH
    KVM
    KVM_INTEL
    KVM_AMD
    VHOST_MENU
    VHOST_NET
    VHOST_SCSI
    VHOST_VSOCK
    VHOST_VDPA
    DEVMEM
    DEVKMEM
    DEVPORT
    PROC_KCORE
    DEBUG_FS
    # Firecracker exposes no workload input or general-purpose hardware buses.
    # Keep its minimal AT-keyboard path below solely for the host-initiated
    # Ctrl-Alt-Del shutdown action, but remove every userspace handler and
    # unrelated input device class.
    INPUT_EVDEV
    INPUT_JOYDEV
    INPUT_MOUSEDEV
    INPUT_MOUSE
    INPUT_JOYSTICK
    INPUT_TABLET
    INPUT_TOUCHSCREEN
    INPUT_MISC
    GAMEPORT
    HID_SUPPORT
    USB_SUPPORT
    SOUND
    MEDIA_SUPPORT
    DRM
    FB
    AGP
    SCSI
    ATA
    BLK_DEV_NVME
    MMC
    MEMSTICK
    MTD
    FIREWIRE
    ETHERNET
    WLAN
    WWAN
    BT
    RFKILL
    NFC
    CAN
    HAMRADIO
    INFINIBAND
    ISDN
    PARPORT
    PCCARD
    # Applications receive ordinary IP sockets, not a programmable network
    # appliance, nested network topology, packet capture, or kernel VPN stack.
    NET_NS
    PACKET
    NETFILTER
    NET_SCHED
    BRIDGE
    VLAN_8021Q
    OPENVSWITCH
    BONDING
    DUMMY
    NET_TEAM
    MACVLAN
    IPVLAN
    VXLAN
    GENEVE
    TUN
    VETH
    PPP
    SLIP
    MACSEC
    WIREGUARD
    L2TP
    PPTP
    NET_IPIP
    NET_IPGRE
    IPV6_SIT
    IPV6_TUNNEL
    IPV6_GRE
    NET_KEY
    XFRM_USER
    INET_AH
    INET_ESP
    INET_IPCOMP
    INET6_AH
    INET6_ESP
    INET6_IPCOMP
    IP_ADVANCED_ROUTER
    IP_MULTIPLE_TABLES
    IP_ROUTE_MULTIPATH
    IP_MROUTE
    IPV6_SEG6_LWTUNNEL
    IPV6_SEG6_HMAC
    IPV6_MULTIPLE_TABLES
    IP_SCTP
    DCCP
    RDS
    TIPC
    ATM
    X25
    LAPB
    PHONET
    IEEE802154
    6LOWPAN
    AF_RXRPC
    MPLS
    NET_NSH
    # The image has one ext4 platform disk, one ext4 container disk, tmpfs
    # upper layers, and an optional NFSv4 workspace. Alternate local,
    # removable-media, clustered, and remote filesystem parsers are outside
    # that mount contract.
    EXT2_FS
    EXT3_FS
    XFS_FS
    GFS2_FS
    OCFS2_FS
    BTRFS_FS
    NILFS2_FS
    F2FS_FS
    JFS_FS
    REISERFS_FS
    FUSE_FS
    CUSE
    CONFIGFS_FS
    AUTOFS_FS
    AUTOFS4_FS
    SQUASHFS
    EROFS_FS
    CRAMFS
    ROMFS
    FAT_FS
    MSDOS_FS
    VFAT_FS
    EXFAT_FS
    NTFS_FS
    NTFS3_FS
    ISO9660_FS
    UDF_FS
    HFS_FS
    HFSPLUS_FS
    UFS_FS
    MINIX_FS
    CIFS
    SMB_SERVER
    CEPH_FS
    CODA_FS
    AFS_FS
    9P_FS
    ORANGEFS_FS
    NFSD
    NFS_V2
    NFS_V3
    NFS_V3_ACL
    # Workloads are native x86-64 ELF files or scripts. Runtime registration
    # of executable formats and legacy 32-bit ABIs unnecessarily multiply the
    # syscall and binary-parser surface.
    BINFMT_MISC
    BINFMT_AOUT
    IA32_EMULATION
    X86_X32
    # Seccomp needs classic BPF internally, not guest-accessible eBPF, tracing,
    # probes, performance counters, or coverage instrumentation.
    BPF_SYSCALL
    BPF_JIT
    KPROBES
    UPROBES
    # x86 selects the PERF_EVENTS core unconditionally. Workloads cannot reach
    # perf_event_open because the guest runner's seccomp policy denies it; the
    # optional hardware backend remains excluded here.
    HW_PERF_EVENTS
    FTRACE
    FUNCTION_TRACER
    FUNCTION_GRAPH_TRACER
    DYNAMIC_FTRACE
    GCOV_KERNEL
    KCOV
    IKCONFIG
    IKCONFIG_PROC
    # The workload gets one host-created mount namespace. Unprivileged nested
    # namespaces are not part of the application contract.
    USER_NS
    CHECKPOINT_RESTORE
    # Firecracker boots the supported virtio PCI transport and describes it
    # through x86 ACPI, so retain the PCI core. It offers no PCI hotplug/EFI
    # boot path, suspend, hibernation, NUMA topology, or
    # guest-controlled frequency scaling.
    HOTPLUG_PCI
    EFI
    EFI_STUB
    SUSPEND
    HIBERNATION
    # x86 enables the HOTPLUG_CPU core whenever SMP is built. SMP remains
    # necessary for multi-vCPU profiles; workloads have neither root nor the
    # capabilities needed to administer the CPU sysfs controls.
    BOOTPARAM_HOTPLUG_CPU0
    NUMA
    SCHED_MC_PRIO
    CPU_FREQ
    ACPI_PROCESSOR
    CPU_IDLE
  )
  for option in "${kernel_disabled_options[@]}"; do
    scripts/config --disable "${option}"
  done
  kernel_required_firecracker_options=(
    HYPERVISOR_GUEST
    KVM_GUEST
    VIRTIO
    VIRTIO_PCI
    VIRTIO_MMIO
    VIRTIO_MMIO_CMDLINE_DEVICES
    VIRTIO_BLK
    VIRTIO_NET
    VSOCKETS
    VIRTIO_VSOCKETS
    MEMORY_HOTPLUG
    MEMORY_HOTREMOVE
    VIRTIO_MEM
    SERIAL_8250
    SERIAL_8250_CONSOLE
    INPUT
    INPUT_KEYBOARD
    KEYBOARD_ATKBD
    SERIO
    SERIO_I8042
    SERIO_LIBPS2
    PCI_MSI
  )
  for option in "${kernel_required_firecracker_options[@]}"; do
    scripts/config --enable "${option}"
  done
  kernel_required_network_options=(
    NET
    UNIX
    INET
    IPV6
    IP_PNP
    SUNRPC
  )
  for option in "${kernel_required_network_options[@]}"; do
    scripts/config --enable "${option}"
  done
  kernel_required_workload_security_options=(
    BPF
    SECCOMP
    SECCOMP_FILTER
  )
  for option in "${kernel_required_workload_security_options[@]}"; do
    scripts/config --enable "${option}"
  done
  kernel_required_runtime_options=(
    BINFMT_ELF
    BINFMT_SCRIPT
    BLK_DEV_INITRD
    CGROUPS
    NAMESPACES
    PROC_FS
    SYSFS
    DEVTMPFS
    DEVTMPFS_MOUNT
    UNIX98_PTYS
    EXT4_FS
    OVERLAY_FS
    TMPFS
  )
  for option in "${kernel_required_runtime_options[@]}"; do
    scripts/config --enable "${option}"
  done
  kernel_required_workspace_options=(
    NETWORK_FILESYSTEMS
    NFS_FS
    NFS_V4
    NFS_V4_1
    NFS_V4_2
  )
  for option in "${kernel_required_workspace_options[@]}"; do
    scripts/config --enable "${option}"
  done
  scripts/config --set-str INITRAMFS_SOURCE fire-fuse-initramfs
  make --silent olddefconfig
  for option in "${kernel_disabled_options[@]}"; do
    case "$(scripts/config --state "${option}")" in
      n | undef) ;;
      *) fail "rebuilt guest kernel unexpectedly enables CONFIG_${option}" ;;
    esac
  done
  for option in "${kernel_required_firecracker_options[@]}"; do
    [[ "$(scripts/config --state "${option}")" == 'y' ]] ||
      fail "rebuilt guest kernel does not build in CONFIG_${option}"
  done
  for option in "${kernel_required_network_options[@]}"; do
    [[ "$(scripts/config --state "${option}")" == 'y' ]] ||
      fail "rebuilt guest kernel does not build in CONFIG_${option}"
  done
  for option in "${kernel_required_workload_security_options[@]}"; do
    [[ "$(scripts/config --state "${option}")" == 'y' ]] ||
      fail "rebuilt guest kernel does not build in CONFIG_${option}"
  done
  for option in "${kernel_required_runtime_options[@]}"; do
    [[ "$(scripts/config --state "${option}")" == 'y' ]] ||
      fail "rebuilt guest kernel does not build in CONFIG_${option}"
  done
  for option in "${kernel_required_workspace_options[@]}"; do
    [[ "$(scripts/config --state "${option}")" == 'y' ]] ||
      fail "rebuilt guest kernel does not build in CONFIG_${option}"
  done
  grep -Fqx 'CONFIG_INITRAMFS_SOURCE="fire-fuse-initramfs"' .config ||
    fail 'rebuilt guest kernel configuration does not embed fire-fuse init'
  make --silent -j "$(nproc)" vmlinux
)

unsquashfs -no-progress -d "${work_directory}/rootfs" "${work_directory}/ubuntu.squashfs" >/dev/null
dpkg-deb --extract "${work_directory}/nfs-common.deb" "${work_directory}/rootfs"
ssh-keygen -q -A -f "${work_directory}/rootfs"
for host_key in ssh_host_ed25519_key ssh_host_ed25519_key.pub; do
  [[ -s "${work_directory}/rootfs/etc/ssh/${host_key}" ]] || \
    fail "guest image does not contain generated SSH host key ${host_key}"
done
[[ "$(stat -c '%a' "${work_directory}/rootfs/etc/ssh/ssh_host_ed25519_key")" == '600' ]] || \
  fail 'guest SSH Ed25519 private host key has an unsafe mode'
[[ -x "${work_directory}/rootfs/usr/bin/curl" ]] || fail 'guest image does not contain /usr/bin/curl'
[[ -x "${work_directory}/rootfs/usr/bin/bash" ]] || fail 'guest image does not contain /usr/bin/bash'
[[ -x "${work_directory}/rootfs/usr/sbin/blockdev" ]] || \
  fail 'guest image does not contain /usr/sbin/blockdev'
[[ -x "${work_directory}/rootfs/usr/bin/date" ]] || fail 'guest image does not contain /usr/bin/date'
[[ -x "${work_directory}/rootfs/usr/bin/dd" ]] || fail 'guest image does not contain /usr/bin/dd'
[[ -x "${work_directory}/rootfs/usr/bin/install" ]] || fail 'guest image does not contain /usr/bin/install'
[[ -x "${work_directory}/rootfs/usr/bin/mount" ]] || fail 'guest image does not contain /usr/bin/mount'
[[ -x "${work_directory}/rootfs/usr/bin/mountpoint" ]] || fail 'guest image does not contain /usr/bin/mountpoint'
[[ -x "${work_directory}/rootfs/usr/bin/rm" ]] || fail 'guest image does not contain /usr/bin/rm'
[[ -x "${work_directory}/rootfs/usr/bin/stat" ]] || fail 'guest image does not contain /usr/bin/stat'
[[ -x "${work_directory}/rootfs/usr/bin/sync" ]] || fail 'guest image does not contain /usr/bin/sync'
[[ -x "${work_directory}/rootfs/usr/bin/systemctl" ]] || fail 'guest image does not contain /usr/bin/systemctl'
[[ -x "${work_directory}/rootfs/usr/bin/tar" ]] || fail 'guest image does not contain /usr/bin/tar'
[[ -x "${work_directory}/rootfs/usr/bin/umount" ]] || fail 'guest image does not contain /usr/bin/umount'
[[ -x "${work_directory}/rootfs/usr/sbin/ip" ]] || fail 'guest image does not contain /usr/sbin/ip'
[[ -x "${work_directory}/rootfs/usr/sbin/mount.nfs4" ]] || \
  fail 'guest image does not contain the pinned NFSv4 mount helper'
install -d -m 0755 "${work_directory}/rootfs/usr/local/sbin"
install -d -m 0755 "${work_directory}/rootfs/usr/local/bin"
install -d -m 0755 "${work_directory}/rootfs/usr/local/lib/cantelop"
install -d -m 0755 "${work_directory}/rootfs/etc/systemd/system"
install -d -m 0755 "${work_directory}/rootfs/workspace"
install -m 0755 "${script_directory}/guest/fire-fuse-mount-workspace" \
  "${work_directory}/rootfs/usr/local/sbin/fire-fuse-mount-workspace"
install -m 0755 "${script_directory}/guest/fire-fuse-wait-workspace" \
  "${work_directory}/rootfs/usr/local/sbin/fire-fuse-wait-workspace"
install -m 0755 "${script_directory}/guest/fire-fuse-wait-workspace-network" \
  "${work_directory}/rootfs/usr/local/sbin/fire-fuse-wait-workspace-network"
install -m 0755 "${script_directory}/guest/fire-fuse-workspace-benchmark" \
  "${work_directory}/rootfs/usr/local/bin/fire-fuse-workspace-benchmark"
install -m 0755 "${work_directory}/fire-fuse-guest" \
  "${work_directory}/rootfs/usr/local/sbin/fire-fuse-guest"
install -m 0755 "${work_directory}/fire-fuse-assignment-agent" \
  "${work_directory}/rootfs/usr/local/sbin/fire-fuse-assignment-agent"
install -m 0755 "${work_directory}/bun-release/bun-linux-x64-baseline/bun" \
  "${work_directory}/rootfs/usr/local/lib/cantelop/bun"
install -m 0755 "${script_directory}/guest/fire-fuse-mount-container-rootfs" \
  "${work_directory}/rootfs/usr/local/sbin/fire-fuse-mount-container-rootfs"
install -m 0755 "${script_directory}/guest/fire-fuse-unmount-container-rootfs" \
  "${work_directory}/rootfs/usr/local/sbin/fire-fuse-unmount-container-rootfs"
install -m 0755 "${script_directory}/guest/fire-fuse-prepare-container-device" \
  "${work_directory}/rootfs/usr/local/sbin/fire-fuse-prepare-container-device"
install -m 0755 "${script_directory}/guest/fire-fuse-record-workload-exit" \
  "${work_directory}/rootfs/usr/local/sbin/fire-fuse-record-workload-exit"
install -m 0755 "${script_directory}/guest/fire-fuse-memory-status" \
  "${work_directory}/rootfs/usr/local/sbin/fire-fuse-memory-status"
install -m 0755 "${script_directory}/guest/fire-fuse-prime-platform-runtime" \
  "${work_directory}/rootfs/usr/local/sbin/fire-fuse-prime-platform-runtime"
install -m 0644 "${script_directory}/guest/fire-fuse-memory-status.socket" \
  "${work_directory}/rootfs/etc/systemd/system/fire-fuse-memory-status.socket"
install -m 0644 "${script_directory}/guest/fire-fuse-memory-status@.service" \
  "${work_directory}/rootfs/etc/systemd/system/fire-fuse-memory-status@.service"
install -m 0644 "${script_directory}/guest/fire-fuse-assignment-agent.service" \
  "${work_directory}/rootfs/etc/systemd/system/fire-fuse-assignment-agent.service"
install -m 0644 "${script_directory}/guest/fire-fuse-assigned-workload.service" \
  "${work_directory}/rootfs/etc/systemd/system/fire-fuse-assigned-workload.service"
install -m 0644 "${script_directory}/guest/fire-fuse-platform-runtime-prime.service" \
  "${work_directory}/rootfs/etc/systemd/system/fire-fuse-platform-runtime-prime.service"
install -m 0644 "${script_directory}/guest/fire-fuse-snapshot.target" \
  "${work_directory}/rootfs/etc/systemd/system/fire-fuse-snapshot.target"
install -d -m 0700 "${work_directory}/rootfs/root/.ssh"
install -m 0600 "${public_key_path}" "${work_directory}/rootfs/root/.ssh/authorized_keys"

rm -f "${work_directory}/rootfs/etc/resolv.conf"
printf 'nameserver 1.1.1.1\nnameserver 8.8.8.8\n' > "${work_directory}/rootfs/etc/resolv.conf"
chmod 0644 "${work_directory}/rootfs/etc/resolv.conf"
systemctl --root="${work_directory}/rootfs" enable ssh.service >/dev/null

truncate -s 1G "${work_directory}/ubuntu-24.ext4"
mkfs.ext4 -q -F -d "${work_directory}/rootfs" "${work_directory}/ubuntu-24.ext4"
rm -f "${work_directory}/rootfs/etc/ssh/ssh_host_"*
printf 'fire-fuse-container-rootfs-v1\n' \
  > "${work_directory}/rootfs/etc/fire-fuse-container-rootfs-id"
install -d -m 0755 "${work_directory}/rootfs/run/sshd"
truncate -s 1G "${work_directory}/container-ubuntu-24.ext4"
mkfs.ext4 \
  -q \
  -F \
  -d "${work_directory}/rootfs" \
  "${work_directory}/container-ubuntu-24.ext4"

release_directory="${work_directory}/release"
install -d -m 0755 "${release_directory}"
install -m 0644 "${kernel_source_directory}/vmlinux" "${release_directory}/vmlinux"
install -m 0644 "${work_directory}/ubuntu-24.ext4" \
  "${release_directory}/ubuntu-24.ext4"
install -m 0644 "${work_directory}/container-ubuntu-24.ext4" \
  "${release_directory}/container-ubuntu-24.ext4"
install -m 0644 "${script_directory}/guest/container-ubuntu-24.process.json" \
  "${release_directory}/container-ubuntu-24.process.json"
(
  cd "${release_directory}"
  sha256sum vmlinux > vmlinux.sha256
  sha256sum ubuntu-24.ext4 > ubuntu-24.ext4.sha256
  sha256sum container-ubuntu-24.ext4 > container-ubuntu-24.ext4.sha256
  sha256sum container-ubuntu-24.process.json > container-ubuntu-24.process.json.sha256
)

install -d -m 0750 "${output_directory}"
publish() {
  local source_path="$1"
  local destination_path="$2"
  pending_source="${source_path}"
  pending_destination="${destination_path}"
  mv -nT -- "${source_path}" "${destination_path}"
  [[ ! -e "${source_path}" ]] ||
    fail "output appeared during publication: ${destination_path}"
  published_paths+=("${destination_path}")
  pending_source=''
  pending_destination=''
}
for release_name in \
  vmlinux vmlinux.sha256 \
  ubuntu-24.ext4 ubuntu-24.ext4.sha256 \
  container-ubuntu-24.ext4 container-ubuntu-24.ext4.sha256 \
  container-ubuntu-24.process.json container-ubuntu-24.process.json.sha256
do
  publication_staging_path="$(
    mktemp "${output_directory}/.${release_name}.tmp.XXXXXX"
  )"
  publication_staging_paths+=("${publication_staging_path}")
  install -m 0644 \
    "${release_directory}/${release_name}" \
    "${publication_staging_path}"
  publish \
    "${publication_staging_path}" \
    "${output_directory}/${release_name}"
done
publication_complete=1

printf 'Prepared pinned Firecracker guest artifacts in %s\n' "${output_directory}"
