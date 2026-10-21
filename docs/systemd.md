# systemd installation

The packaged unit targets a modern Linux distribution with systemd 245 or
newer. It uses the filesystem layout from the
[operator runbook](operations.md):

- binary: `/usr/local/bin/fire-fuse`;
- configuration: `/etc/fire-fuse/config.json`; and
- state, images, and runtime jails: `/var/lib/fire-fuse`.

Customer Build materialization additionally requires Docker Engine with the
CLI at `/usr/bin/docker` and `mkfs.ext4` at `/usr/sbin/mkfs.ext4`. The packaged
unit requires and starts after `docker.service`; it never starts a customer
container and uses Docker only for archive load, non-running container create,
and filesystem export.

The optional App ingress uses two additional components:

- gateway binary: `/usr/local/bin/fire-fuse-gateway`;
- gateway unit: `/etc/systemd/system/fire-fuse-gateway.service`; and
- outbound-only Cloudflare Tunnel connector to the gateway at
  `127.0.0.1:8081`.

The gateway, observer, and asynchronous dispatcher are local Fire Fuse
dependents. The durable control worker runs on the separate control/build VM
and reaches provider ingress over WireGuard. A Fire Fuse restart must not stop
or restart that remote worker; its PostgreSQL operations retry while compute
is unavailable.

## Install

Build the Linux binary, then install it and the service definition:

```sh
go build -o fire-fuse ./fire-fuse/runtime/cmd/fire-fuse
sudo install -m 0755 fire-fuse /usr/local/bin/fire-fuse
sudo install -d -m 0750 /etc/fire-fuse
sudo install -d -o root -g root -m 0700 /etc/fire-fuse/control-clients
sudo install -d -m 0750 /var/lib/fire-fuse/images
sudo install -d -m 0700 /var/lib/fire-fuse/build-uploads
sudo install -d -m 0700 /var/lib/fire-fuse/build-work
sudo install -d -m 0755 /usr/local/share/doc/fire-fuse
sudo install -m 0600 config.example.json /etc/fire-fuse/config.json
sudo install -m 0600 image-catalog.example.json /etc/fire-fuse/image-catalog.json
for role in operator gateway dispatcher observer compute; do
  openssl rand -hex 32 | sudo tee "/etc/fire-fuse/control-clients/${role}.token" >/dev/null
done
sudo chmod 0600 /etc/fire-fuse/control-clients/*.token
printf '%s\n' \
  'CANTELOP_ARTIFACT_DOWNLOAD_ORIGIN=https://replace-with-account-id.r2.cloudflarestorage.com' |
  sudo tee /etc/fire-fuse/artifact-store.env >/dev/null
sudo chmod 0600 /etc/fire-fuse/artifact-store.env
sudo install -m 0644 \
  docs/api-lifecycle.md docs/docker-images.md docs/openapi.yaml \
  docs/operations.md docs/systemd.md \
  /usr/local/share/doc/fire-fuse/
sudo install -m 0644 platform/systemd/compute/fire-fuse.service \
  /etc/systemd/system/fire-fuse.service
```

Install Firecracker, its matching jailer, the kernel, and both rootfs images at
the absolute paths selected in `config.json`. Review the complete host and
configuration checklist in the operator runbook before starting the unit.

The unit's `ConfigurationDirectory=` and `StateDirectory=` directives also
create `/etc/fire-fuse`, `/var/lib/fire-fuse/state`, and
`/var/lib/fire-fuse/runtime` whenever the service starts. They do
not populate `config.json`, the control token, the artifact-store environment,
or boot artifacts. The artifact download origin must exactly match the HTTPS
origin used by the control worker's signed artifact URLs.

Use `/var/lib/fire-fuse/state/state.db` for `state_db` and
`/var/lib/fire-fuse/runtime` for `runtime_dir`, as in the example configuration.
Existing installations using other paths must preserve and deliberately migrate
their state before changing these settings; never start against an empty
database as a substitute. Custom paths need corresponding writable allowances.
Keep those allowances disjoint from the workspace mount: do not grant writable
access to the common `/var/lib/fire-fuse` parent, including through
`StateDirectory=fire-fuse`. With `ProtectSystem=strict`, that parent bind
duplicates its JuiceFS child in the service mount table and fails the strict
workspace policy. The app-visible durable path remains `/workspace`.

On a provisioned host, exercise the packaged filesystem isolation without
starting another Fire Fuse daemon:

```sh
sudo env CANTELOP_TEST_SYSTEMD_MOUNTS=1 python3 -m unittest discover \
  -s platform/ansible/tests -p test_fire_fuse_service_mounts.py -v
```

This runs a transient systemd service that only reads its mount table. It checks
one protected writable workspace mount, writable runtime directories, and a
read-only root. Run it from the candidate checkout before installing the unit;
normal host readiness alone does not exercise workspace attachment.

On the target Linux host, verify the unit and activate it:

```sh
sudo systemd-analyze verify /etc/systemd/system/fire-fuse.service
sudo systemctl daemon-reload
sudo systemctl enable --now fire-fuse.service
sudo systemctl status fire-fuse.service
curl --fail http://127.0.0.1:8080/readyz
```

Follow the combined fire-fuse and child-runtime output with:

```sh
sudo journalctl -u fire-fuse.service -f
```

## App gateway

Use the checksum-pinned Ansible deployment in [gateway.md](gateway.md) after
the base service is running. The gateway unit is ordered after and requires
`fire-fuse.service`, runs as the dedicated `fire-fuse-gateway` user, and can
only reach the loopback control plane plus IP Sandbox targets. The Cloudflare
Tunnel connector reaches it over loopback; neither the gateway nor Fire Fuse
runs on a public listener.

Inspect the deployed services with:

```sh
sudo systemctl status fire-fuse-gateway.service cloudflared.service
curl --fail http://127.0.0.1:8081/readyz
sudo journalctl -u fire-fuse-gateway.service -f
```

## Shutdown contract

`KillMode=mixed` is essential for ownership safety. systemd sends the initial
SIGTERM only to the fire-fuse main process, allowing it to drain HTTP work,
terminate Firecracker, checkpoint each released resource, and close SQLite. If
the main process has not completed within `TimeoutStopSec=2min`, systemd sends
SIGKILL to every process left in the unit cgroup. This follows the
[`KillMode=mixed` contract](https://www.freedesktop.org/software/systemd/man/latest/systemd.kill.html#KillMode=).

The configured worst-case stop window is approximately:

```text
server_shutdown + guest_shutdown + shutdown_grace + shutdown_force + cleanup + overhead
```

If configuration raises that sum near two minutes, install a drop-in with a
larger outer deadline:

```sh
sudo systemctl edit fire-fuse.service
```

```ini
[Service]
TimeoutStopSec=5min
```

Do not change `KillMode` to `control-group`: doing so sends the initial SIGTERM
to Firecracker children at the same time as fire-fuse and bypasses orderly
supervisor sequencing.

## Privilege and hardening choices

The service deliberately runs as root. Firecracker's jailer requires elevated
operations including mount namespaces, `pivot_root`, `chroot`, device-node
creation, ownership changes, and access to KVM and TUN. fire-fuse additionally
manages host TAP interfaces and iptables rules. The non-root
`jailer_uid`/`jailer_gid` remain the identity to which jailer drops Firecracker.
These operations and the upstream root boundary are described in Firecracker's
[jailer documentation](https://github.com/firecracker-microvm/firecracker/blob/main/docs/jailer.md).

The unit narrows access where compatible with those duties:

- only `/dev/kvm`, `/dev/net/tun`, and `/dev/userfaultfd` are added to the
  closed device policy, including the `mknod` access jailer needs to recreate
  them inside a chroot;
- the host filesystem is read-only except managed state and cgroup paths;
- home directories and the host temporary directory are isolated;
- kernel tunables, modules, logs, hostname, and clock are protected;
- address families are limited to Unix, IP, IPv6, and netlink sockets.

`NoNewPrivileges` remains disabled. On the qualified Ubuntu 24.04 host,
enabling it allowed jailer to construct the chroot but made its final
UID/GID-transitioned Firecracker `exec` fail with `EPERM`. This restores normal
Linux exec semantics rather than granting a capability by itself. The
root-owned, non-writable executable hierarchy, strict trusted-path validation,
loopback API, closed device policy, jailer privilege drop, chroot, and
Firecracker seccomp boundary remain the compensating controls.

The unit intentionally does not use `PrivateUsers`, `PrivateDevices`, namespace
restrictions, read-only cgroups, or a guessed capability bounding set. Those
settings conflict with known jailer operations or require real-host evidence
before they can be considered safe. Firecracker upstream currently documents
running jailer as root and does not define a stable minimal capability set.

Run this on the target host after startup to inspect remaining exposure:

```sh
sudo systemd-analyze security fire-fuse.service
```

A high numeric exposure score is expected while root and `CAP_SYS_ADMIN` remain
necessary; do not copy suggested directives blindly without rerunning the real
KVM lifecycle and restart-recovery integration tests.

## Overrides

Use a drop-in rather than editing the installed unit. For example, changing the
loopback port requires clearing and replacing `ExecStart`:

```ini
[Service]
ExecStart=
ExecStart=/usr/local/bin/fire-fuse -config /etc/fire-fuse/config.json -listen 127.0.0.1:9090
```

After any override:

```sh
sudo systemd-analyze verify fire-fuse.service
sudo systemctl daemon-reload
sudo systemctl restart fire-fuse.service
curl --fail http://127.0.0.1:9090/readyz
```
