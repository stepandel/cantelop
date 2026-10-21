# Security and ownership audit

The original audit covered fire-fuse commit `463d32c`. A source-level follow-up
at commit `cc582b6` rechecked every finding after remediation. A target-host
qualification through commit `1e7d78d` then exercised the packaged service and
real Firecracker lifecycle. These reviews cover the trust boundaries relevant
to running the service as root on one Linux host: API exposure, configuration
and path handling, lifecycle persistence, PID reuse, jail and network
ownership, recovery cleanup, logging, and the packaged systemd unit.

At the source follow-up commit, the full unit suite, race suite, vet, Linux
AMD64 cross-build, and Linux-only integration-test cross-compilation passed.
The target-host run subsequently passed the real KVM suite, packaged systemd
lifecycle, forced orchestrator crash recovery, normal shutdown, and resource
residue checks described below.

## Result

No critical remote vulnerability was found. The loopback-only API, strict
lifecycle records, pidfd-based recovery, and fail-closed orphan detection form a
sound minimal design.

All actionable findings are resolved for the initial single-host scope, and the
follow-up found no new critical, high, or medium source finding. FFSA-004
remains an explicitly accepted minimal-orchestrator constraint rather than an
untracked defect. The target-host run closes FFSA-005.

## Findings

Each finding description preserves the condition observed at the original
`463d32c` baseline. Its status and the follow-up evidence below describe the
current implementation.

### FFSA-001: TAP cleanup does not verify positive ownership

**Severity:** medium

**Status:** resolved by `ad25bda`

`network.TAPManager.Delete` derives a deterministic TAP name from the VM ID and
immediately runs `ip link delete`. It does not read and validate the
`fire-fuse:<vm-id>` interface alias first. Recovery validates the persisted TAP
name, and orphan inventory validates aliases, but normal and recovery cleanup
both eventually call this unchecked delete operation.

If a recorded fire-fuse TAP disappears and another interface later occupies the
same deterministic name, fire-fuse can delete that replacement. Creating such a
replacement normally requires `CAP_NET_ADMIN`, so this is not an unprivileged
remote attack. It is nevertheless a violation of the project's rule that
cleanup only removes positively identified resources.

**Required remediation:** make lifecycle cleanup read and validate the exact
alias immediately before deletion. A missing interface remains an idempotent
success; a missing or mismatched alias must fail closed. Creation rollback needs
a separate internal deletion path because it may run before the alias has been
installed.

### FFSA-002: trusted filesystem hierarchy is assumed, not enforced

**Severity:** high when an unprivileged account can write a configured parent;
otherwise configuration hardening

**Status:** resolved by `1c996bf`, `8212209`, `1d6a53c`, `539401e`, and
`917698a`

Configuration validation requires clean absolute paths. Preflight then uses
`os.Stat`, `os.Open`, and a runtime-directory write probe. These checks follow
symlinks and do not verify that configured files and their resolved parent
directories are root-owned and not group/world-writable.

The affected paths include:

- the Firecracker, jailer, `ip`, and `iptables` executables;
- kernel, platform-rootfs, and container-rootfs source images;
- `runtime_dir` and its derived jail hierarchy;
- the configuration file; and
- the SQLite state database and its parent directory.

This matters because the service runs as root. Asset validation and copying are
separate pathname operations, the SQLite database is reopened by pathname after
its mode is set, and jail cleanup recursively removes a VM directory derived
under `runtime_dir`. A writable or replaceable parent can therefore turn an
otherwise safe derived path into a privileged read, process launch, state, or cleanup
target. The SQLite lock file already rejects a final symlink with `O_NOFOLLOW`,
and runtime manifests do the same, but the database and broader hierarchy do
not receive equivalent protection.

The packaged systemd layout substantially mitigates this finding: systemd
creates `/etc/fire-fuse` as a root-owned `0750` directory and
`/var/lib/fire-fuse` as a root-owned `0751` directory. The execute-only access
on the state-directory parent lets the dedicated Redis and MinIO service
accounts traverse to their separately protected subdirectories without
exposing a directory listing. The unit also applies `UMask=0077` and makes the
rest of the filesystem read-only. Custom or manually launched deployments are
not currently rejected when they violate those assumptions.

**Required remediation:** add a shared preflight policy that resolves configured
paths, rejects unsafe final symlinks, and verifies ownership and write bits on
the relevant files and parent directories. The state database must reject a
final symlink and require a trusted parent before SQLite opens it. The policy
should accommodate legitimate root-controlled distribution symlinks by
validating their resolved targets rather than banning all symlink components
indiscriminately.

### FFSA-003: HTTP request bodies have no read deadline

**Severity:** low

**Status:** resolved by `cc582b6`

The HTTP server sets `ReadHeaderTimeout`, and machine creation limits request
bodies to 1 MiB, but no server or per-body read deadline bounds a client that
sends the body slowly. Because the listener is restricted to numeric loopback
addresses, exploitation requires local host access. Admission limits constrain
VM work but do not constrain idle HTTP connections.

**Recommended remediation:** add an idle timeout and a body-read deadline whose
interaction with long, synchronous VM startup requests is covered by tests. Do
not set a generic write deadline shorter than the configured lifecycle timeout.

### FFSA-004: one jail identity is shared by all microVMs

**Severity:** low; accepted minimal-orchestrator constraint

**Status:** accepted minimal-orchestrator constraint

All VMs use the configured `jailer_uid` and `jailer_gid`. The jailer still
provides chroot, namespaces, cgroups, seccomp, and privilege dropping, but a
unique UID/GID per VM would add another host isolation boundary. Allocating and
persisting host identities would materially expand the orchestrator and is not
required for the initial single-host minimal scope.

For now, reserve the configured identity exclusively for Firecracker guests and
do not use it for unrelated host services. The per-workspace NFS boundary does
not change this shared host identity. Revisit per-VM identities before claiming
isolation against mutually hostile tenants.

## Workspace filesystem boundary follow-up

The host now mounts one JuiceFS volume and exposes only
`<workspace.root>/<workspace_id>` to each guest through an exact NFS export.
The matching firewall allowance requires the assigned TAP device, assigned
guest source address, TCP port 2049, and a new or established connection.
JuiceFS backend credentials and its metadata URL remain on the host.

This establishes a filesystem-to-guest boundary between different workspace
IDs and allows intentional sharing by equal IDs. It is not a complete tenant
authorization boundary: the loopback API still accepts a caller-selected
workspace ID, and the orchestrator does not map authenticated principals to
workspace ownership. Storage quotas, per-tenant I/O limits, and per-VM jail
identities also remain outside the minimal orchestrator.

### FFSA-005: packaged hardening is not target-host verified

**Severity:** verification gap

**Status:** resolved by the target-host acceptance run through `1e7d78d`

The unit intentionally retains real root, namespace operations, writable cgroup
access, and the capabilities needed by the jailer and network setup. The
target-host run validated its device policy, address-family restriction,
filesystem protections, lifecycle behavior, and restart recovery with real
guests. `NoNewPrivileges=yes` was proven incompatible with jailer's final
UID/GID-transitioned exec and is now explicitly disabled with compensating
trusted-path and sandbox controls documented in `docs/systemd.md`.

**Verification performed:** on the supported Linux distribution, run
`systemd-analyze verify`, inspect `systemd-analyze security`, and exercise real
boot, terminate, forced shutdown, and restart recovery through the installed
unit. Treat a directive that blocks a required jailer operation as a test
failure, not as a reason to weaken unrelated controls.

## Confirmed controls

The following boundaries are implemented correctly in the reviewed code:

- The API accepts only numeric loopback listener addresses and rejects wildcard,
  hostname, non-loopback, and port-zero values.
- API JSON is strict, request bodies are size-limited, Sandbox IDs have a narrow
  grammar, and SQL statements use parameters.
- Admission limits bound VM count, aggregate guest memory, and concurrent
  starts.
- SQLite uses one process-wide nonblocking file lock, one clean schema, strict
  record decoding, index-to-record consistency checks, WAL journaling, and full
  synchronous durability.
- Runtime paths are derived from validated configuration and VM ID rather than
  accepted from persisted records. Runtime manifests are size-limited, strict,
  nonsymlinked, permission-checked, and identity-checked.
- Recovery opens a pidfd before verifying the persisted boot ID and process
  start time. It signals only through that stable handle, preventing numeric PID
  reuse between verification and signaling.
- Runtime ownership is checkpointed after each acquisition and release.
  Recovery stops the verified process before removing forwarding, TAP, address,
  and jail resources.
- Firewall cleanup deletes exact rules derived from the persisted allocation and
  ownership comment. Orphan inspection reports tagged or ambiguous resources
  and blocks startup without deleting them.
- Request logs use matched routes and omit query strings, bodies, authorization
  headers, and internal error text. Documentation treats combined child and
  service output as sensitive.
- The systemd unit uses `KillMode=mixed`, allowing fire-fuse to perform orderly
  cleanup before systemd force-kills remaining child processes at the unit
  deadline.

## Residual operating assumptions

- fire-fuse is a single-host, single-process root service. The API is not safe to
  expose beyond loopback without a separate authenticated transport boundary.
- The host operator owns configuration, boot images, binaries, state, network
  policy, and the jailer UID/GID.
- Firecracker and jailer versions, kernel/platform/container fixtures, and host
  packages are supplied and patched by the operator.
- Guest console and child runtime output can contain workload secrets and must be
  protected as sensitive journal data.

These assumptions align with Firecracker's upstream
[jailer](https://github.com/firecracker-microvm/firecracker/blob/main/docs/jailer.md)
and
[production-host](https://github.com/firecracker-microvm/firecracker/blob/main/docs/prod-host-setup.md)
guidance: the jailer is a privileged boundary, and the directories containing
its binaries and chroot resources must not be writable by unprivileged users.

## Follow-up verification evidence

### FFSA-001

Lifecycle TAP deletion now reads the interface alias and requires the exact
`fire-fuse:<vm-id>` value before invoking `ip link delete`. A missing interface
is idempotent success; an unreadable or mismatched alias fails without issuing a
delete command. Creation rollback has a separate private deletion path because
alias installation can itself be the failed operation. Tests cover all four
boundaries.

### FFSA-002

The shared host-path validator checks both the configured path hierarchy and
the resolved target hierarchy. Directories must be root-owned and not
group/world-writable. Executables, boot images, and configuration must resolve
to root-owned, regular, non-writable files. Root-controlled symlinks are
allowed, but a symlink beneath an unsafe parent is rejected.

Runtime-directory trust is established before the create/chown probe runs. The
SQLite parent receives the same trust check; the final database file is opened
with `O_NOFOLLOW`, validated by descriptor as regular and owned by the effective
service UID, and set to mode `0600`. Configuration trust is established before
strict JSON decoding. The privileged service and real-host integration suite
both use trusted configuration loading.

### FFSA-003

The required `http_request_read` duration configures `http.Server.ReadTimeout`,
bounding headers and request bodies. The required `http_idle` duration bounds
inactive keep-alive connections. The existing shorter header timeout remains.
`WriteTimeout` deliberately remains zero so a synchronous create response is
governed by the configured VM startup deadline rather than an unrelated shorter
server deadline. Configuration and server-construction tests verify these
properties.

### FFSA-005

The qualification ran on a Hetzner AX42-1 dedicated host with AMD-V and the
following software:

- Ubuntu 24.04;
- Linux `6.8.0-134-generic`;
- systemd `255.4-1ubuntu8.16`;
- Firecracker `v1.15.1`; and
- jailer `v1.15.1`.

`systemd-analyze verify` reported no unit errors. The final
`systemd-analyze security` result was `6.9 MEDIUM`; the score remains an
informational exposure inventory because the service deliberately retains the
root operations required by jailer, TAP, iptables, and cgroup management.

The real KVM suite passed prerequisite, lifecycle, guest SSH reachability,
Firecracker-process crash, and orchestrator restart-recovery tests. Normal API
creation returned a durable `running` VM, guest TCP port 22 became reachable,
and API termination returned `terminated` with no Firecracker process left.

With another VM running, `systemctl kill --kill-whom=main --signal=SIGKILL
fire-fuse.service` killed only the orchestrator main process. systemd restarted
the service with a new PID; startup recovery transitioned the interrupted VM to
`terminated` and cleared its runtime ownership before `/readyz` returned. The
post-recovery inventory found zero Firecracker processes, tagged TAP devices,
tagged firewall rules, and runtime manifests. A final normal stop returned a
successful unit result with the same zero-residue inventory and no permission,
cleanup, or sandbox denial in the stop-window journal. The service was then
started again and returned ready.

The qualification exposed and remediated four packaging/runtime mismatches:

- the integration socket hierarchy needed a shorter path on an executable
  trusted mount (`b2bd66b`, `8f403dd`);
- the jailer UID needed traversal access to the root-owned binary directory
  (`965217b`);
- the closed device policy needed `mknod` access for KVM, TUN, and userfaultfd
  (`7c0b979`, `8fcd3dd`); and
- jailer's final privilege transition required `NoNewPrivileges=no`
  (`1e7d78d`).

## Target-host acceptance checklist

Run this checklist on the supported Linux host with KVM, systemd, Firecracker,
jailer, `ip`, `iptables`, and known-good kernel/platform/container fixtures.
Record the OS, kernel, systemd, Firecracker, and jailer versions with the
results.

1. Install the binary, unit, root-owned configuration, and boot images using
   `docs/systemd.md`. Include `http_request_read` and `http_idle` in the strict
   configuration.
2. Run `sudo systemd-analyze verify fire-fuse.service`; require zero errors.
3. Run `sudo systemd-analyze security fire-fuse.service`; retain the report and
   review every exposure rather than treating its numeric score as pass/fail.
4. Run the real integration suite from `README.md`; require boot, terminate,
   restart recovery, and process-crash tests to pass.
5. Start the packaged unit and require both `/healthz` and `/readyz` to return
   HTTP 200 on loopback.
6. Create a VM through the HTTP API, observe `running`, terminate it, and verify
   its Firecracker process, jail, TAP, and exact tagged firewall rules are gone.
7. Create another VM, run
   `sudo systemctl kill --kill-whom=main --signal=SIGKILL fire-fuse.service`,
   allow `Restart=on-failure` to restart it, and require startup recovery to
   remove the interrupted process and all recorded resources before readiness
   returns.
8. Stop the unit normally and require a zero service result, no remaining unit
   processes, no fire-fuse-owned host resources, and no cleanup error in the
   journal.
9. Inspect the journal for systemd sandbox or permission denials affecting
   jailer setup, KVM/TUN access, cgroups, TAP setup, iptables, shutdown, or
   recovery.

FFSA-005 was closed after this checklist passed on the actual deployment
target. Hardening directives that blocked required jailer operations were
adjusted narrowly, documented, and followed by complete lifecycle and restart
checks.
