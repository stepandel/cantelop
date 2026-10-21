# Hetzner dedicated staging deployment

This deployment targets an x86-64 Hetzner dedicated root server booted directly
into Ubuntu 24.04. Hetzner Cloud servers are not suitable because they do not
expose nested virtualization.

The installed API remains bound to `127.0.0.1:8080`. Reach it through an SSH
tunnel; do not expose it directly. Fire Fuse requires its private control
bearer token, but it does not provide TLS and `/readyz` remains available to a
direct loopback health probe.

## 1. Build the Linux binaries

From the repository on a machine with the Go release declared by
[`go.mod`](../../go.mod), or a compatible newer release:

```sh
./scripts/build-compute-linux-amd64.sh
```

This produces the orchestrator, the offline snapshot builder, the real-host
integration test binary, and checksums under `dist/`. Prepare the matching
Firecracker and jailer pair from the checksum-pinned official release selected
by [`prepare-firecracker-linux-amd64.sh`](../../scripts/prepare-firecracker-linux-amd64.sh):

```sh
./scripts/prepare-firecracker-linux-amd64.sh
```

This verifies the release archive and writes both Linux x86-64 binaries and
their individual checksums under `dist/`.

The guest kernel must support Linux IP autoconfiguration. For the staging
qualification, the rootfs must start a TCP service such as SSH so the test can
prove the guest completed boot and is reachable.

On the Linux staging host, install `build-essential`, `bison`, `curl`,
`dpkg-dev`, `e2fsprogs`, `flex`, `libelf-dev`, `libssl-dev`,
`squashfs-tools`, `systemd`, and `tar`, then create the pinned Firecracker CI
guest fixtures. The script verifies the embedded SHA-256 hashes, rebuilds the
pinned Firecracker kernel configuration with built-in NFSv4.2 client support,
injects only the selected client public key, generates the qualification-only
SSH server host keys, enables SSH, and produces separate platform and container
ext4 images used by the installer:

```sh
sudo ./platform/hetzner/prepare-guest.sh \
  ~/.ssh/id_ed25519.pub \
  ./artifacts
```

The generated server host keys are part of the immutable platform fixture and
are shared by VMs restored from its snapshot. SSH is reachable only on the
private guest network and exists for operator qualification; customer workload
the production runtime does not use it. The integration client deliberately does not treat
these fixture keys as guest identity.

If the Linux image-building host deliberately has no Go toolchain, cross-build
the three static guest helpers on the reviewed build machine and supply their
absolute directory instead. The image builder still performs the kernel and
rootfs assembly locally:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false \
  -ldflags='-s -w' -o ./dist/guest/fire-fuse-guest ./fire-fuse/guest-runtime/cmd/fire-fuse-guest
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false \
  -ldflags='-s -w' -o ./dist/guest/fire-fuse-assignment-agent \
  ./fire-fuse/guest-runtime/cmd/fire-fuse-assignment-agent
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false \
  -ldflags='-s -w' -o ./dist/guest/fire-fuse-init ./fire-fuse/guest-runtime/cmd/fire-fuse-init

sudo FIRE_FUSE_PREBUILT_GUEST_DIRECTORY="$PWD/dist/guest" \
  ./platform/hetzner/prepare-guest.sh ~/.ssh/id_ed25519.pub ./artifacts
```

The rebuilt kernel is deliberately a Firecracker application kernel rather
than a general-purpose distribution kernel. It retains native x86-64
ELF/scripts, cgroups and mount namespaces, ext4, OverlayFS, tmpfs,
proc/sysfs/devtmpfs/devpts, NFSv4.1/4.2, seccomp, virtio block/network, and
IPv4/IPv6 application sockets. It excludes alternate filesystem parsers and
file servers, FUSE, runtime executable-format registration, 32-bit ABIs,
unprivileged user namespaces, eBPF syscalls, tracing/perf/probes, PCI/EFI,
suspend/hibernation, PCI hotplug, NUMA, and CPU-frequency facilities. It retains
virtio-mem and guest memory hotplug for elastic sandbox memory. Expanding that
contract requires an explicit kernel configuration change and a rebuilt,
requalified artifact.

The output directory also receives the fixture's resolved process JSON and
`.sha256` files for the rebuilt kernel, platform rootfs, container rootfs, and
process contract. Each sidecar uses the artifact's local filename, so it can
be checked from that directory with `sha256sum --check`. Use those exact
digests and paths as the corresponding Ansible staging inputs.

Once those artifacts and the host configuration are installed, build a generic
snapshot bundle offline. The placeholder must be a trusted root-owned ext4
file; it fixes the `/dev/vdb` device shape captured by the snapshot but is not
the image used for a restored workload.

```sh
sudo ./dist/fire-fuse-snapshot-linux-amd64 \
  -config /etc/fire-fuse/config.json \
  -placeholder /var/lib/fire-fuse/images/container-placeholder.ext4 \
  -output /var/lib/fire-fuse/snapshots/default-4vcpu-512m-hotplug-4g
```

The output directory must not exist. The command boots only to
`fire-fuse-snapshot.target`, waits for the assignment agent barrier, pauses the
VM, creates a full snapshot, and atomically publishes the state, memory, and
compatibility manifest.

To opt a profile into restore, add the bundle directory to its trusted host
configuration and restart fire-fuse:

```json
{
  "profiles": {
    "default": {
	      "snapshot_bundle": "/var/lib/fire-fuse/snapshots/default-4vcpu-512m-hotplug-4g"
    }
  }
}
```

Keep the profile's kernel, platform rootfs, and boot arguments alongside this
required field. At startup, fire-fuse checks the exact Firecracker version and
host CPU identity and verifies the state, memory, kernel, and platform-rootfs
sizes and SHA-256 values. Requests whose vCPU or memory dimensions do not match
the captured machine are rejected with `unsupported_shape`.

On restore, fire-fuse binds the requested catalog image at the captured
`/dev/vdb` path, loads the VM paused with the request's TAP override, refreshes
the drive backing file, and resumes. The guest then receives its
unique network, workload, and optional workspace assignment over vsock. The
bundle remains generic and can therefore be shared by profiles whose kernel and
platform rootfs identities match even when their container images differ.

The platform image contains `/workspace`, the static workload runner, the
snapshot assignment agent, and `fire-fuse-snapshot.target`. On assignment, the
agent writes the image process settings and any workspace coordinates,
including the unique assignment ID, into root-only files under `/run`, mounts
the requested container image from `/dev/vdb`, mounts the NFS export at
`/workspace` when the assignment enables a workspace, and then starts
`fire-fuse-assigned-workload.service`. The mount script sets the assignment ID
as the NFSv4 client identifier before the first mount; snapshot clones
otherwise share a hostname and can steal each other's NFS lease.
Workspace-disabled assignments skip the workspace mount.

For an application image, use
[`prepare-docker-image.sh`](prepare-docker-image.sh) as described in
[`docs/docker-images.md`](../../docs/docker-images.md), then declare the
resulting ext4 and process JSON as one `fire_fuse_release_images` entry. Staging
derives its immutable live path and catalog ID.

## 2. Order a dedicated server

Enable ordering under Robot **Administration > Settings > Web Service
Settings > Ordering**, then export Robot credentials without committing them:

```sh
export HETZNER_ROBOT_USER='...'
export HETZNER_ROBOT_PASSWORD='...'
```

Inspect the current standard catalog and exact distribution strings:

```sh
./platform/hetzner/robot-server.sh products | jq .
```

List the SSH keys already registered with Robot and select the fingerprint that
should be installed for root access:

```sh
./platform/hetzner/robot-server.sh keys | jq .
```

If the account has no suitable key, register a local public key:

```sh
./platform/hetzner/robot-server.sh add-key fire-fuse-staging ~/.ssh/id_ed25519.pub | jq .
```

Prepare an order. It is a Robot API validation request until `--execute` is
present:

```sh
export HETZNER_PRODUCT_ID='EX44-1-LTD'
export HETZNER_LOCATION='FSN1'
export HETZNER_DIST='Ubuntu 24.04 LTS base'
export HETZNER_SSH_KEY_FINGERPRINT='...'
export HETZNER_ADD_IPV4=1

./platform/hetzner/robot-server.sh order | jq .
./platform/hetzner/robot-server.sh order --execute | jq .
./platform/hetzner/robot-server.sh order-status TRANSACTION_ID | jq .
```

Limited products can be unavailable. Select a product ID returned by the API
rather than hard-coding an assumed offer.

## 3. Check host networking

Before choosing the microVM pool, inspect every route and locally managed
network:

```sh
ip -4 address
ip -4 route
docker network ls 2>/dev/null || true
```

The default staging pool is `172.31.0.0/16`. Override it if that range overlaps
Hetzner private networking, a VPN, Docker, or another route.

## 4. Deploy and qualify

The Ansible workflow is the sole host deployment path. It provisions the base
host, storage services, workspace gateway, snapshot bundle, runtime, and
qualification checks. Follow the ordered staging procedure in
[`platform/ansible/README.md`](../ansible/README.md).

Do not enable systemd `PrivateDevices` or `PrivateNetwork`; they hide `/dev/kvm`
or the host network. The managed unit is described in
[`docs/systemd.md`](../../docs/systemd.md).

## 5. Private API access

```sh
ssh -L 8080:127.0.0.1:8080 root@SERVER_IP
curl http://127.0.0.1:8080/readyz
```

Run the Ansible `playbooks/compute/edge-firewall.yml` provider boundary. It uses the
Robot API to drop public TCP/UDP ports 111 and 2049 for IPv4 and IPv6, refuses
to overwrite an independently managed Robot ruleset, and verifies the exact
active policy after an update. Keep its Webservice credentials in Ansible Vault.
The host also masks rpcbind and restricts NFS locally as defense in depth.

The managed provider boundary intentionally leaves other traffic unchanged.
Restrict SSH to approved durable source CIDRs in a separately reviewed Robot
policy; do not infer a permanent allowlist from the IP used during deployment.
The guests use private `/30` networks and outbound NAT, so they do not require
additional Hetzner public addresses. Host firewall rules default-deny guest
egress, guest-to-host access, source spoofing, unsolicited forwarding, and all
IPv6. The staging policy permits DNS only through `1.1.1.1` and `8.8.8.8`,
TCP ports 22/80/443, and UDP port 443; private, link-local, carrier-grade NAT,
benchmark, multicast, and reserved IPv4 destinations remain denied before the
port allowlist. Add application-specific public destination ports to the
host-owned `network.egress` configuration rather than widening guest kernel
authority. Workspace NFS is authorized separately for the exact TAP and guest
address.

## 6. Release the server

Query cancellation data first, then explicitly execute immediate cancellation:

```sh
./platform/hetzner/robot-server.sh cancel SERVER_NUMBER | jq .
./platform/hetzner/robot-server.sh cancel SERVER_NUMBER --execute | jq .
```

Immediate cancellation deletes host data. Preserve logs and test reports before
executing it. Setup fees, when present, apply again to future orders.
