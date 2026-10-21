# Operator runbook

This runbook covers the current Linux deployment. The packaged service is
described in [systemd.md](systemd.md), and API semantics are defined in
[api-lifecycle.md](api-lifecycle.md).

The workspace storage backup unit, consistency boundary, provisional retention,
and restore qualification are defined in
[storage-backup.md](storage-backup.md). Backup scheduling remains external to
the fire-fuse process.

## Security and privilege boundary

fire-fuse exposes a bearer-authenticated lifecycle API only on a numeric
loopback address. Its 64-lowercase-hex control token is a host secret shared
only with trusted local components. Do not place a TCP proxy in front of the
API or forward its port to another host; the bearer token does not replace a
reviewed transport-security boundary.

The orchestrator process does not drop its own privileges. It needs to:

- open `/dev/kvm` read-write;
- execute Firecracker's jailer;
- create, configure, and delete TAP interfaces;
- add, inspect, and delete iptables rules;
- create VM jails and change their ownership; and
- signal and inspect supervised processes.

On a conventional Firecracker host, the supported manual baseline is therefore
to run fire-fuse as root. A future service unit must preserve these abilities
explicitly rather than assuming that an arbitrary unprivileged account works.

`jailer_uid` and `jailer_gid` are not the orchestrator's service identity. They
are the required non-root identity passed to jailer and assigned to each TAP and
private jail asset. Use a dedicated numeric system identity rather than a human
login, and never configure either value as zero.

## Host prerequisites

Use a Linux host with hardware virtualization enabled and verify all of the
following before installation:

- `/dev/kvm` exists as a character device and the service identity can open it
  read-write.
- The configured Firecracker, jailer, `ip`, and `iptables` paths are readable,
  executable regular files.
- IPv4 forwarding is already enabled; `/proc/sys/net/ipv4/ip_forward` must
  contain `1`.
- The filter `FORWARD` and NAT `POSTROUTING` iptables chains are available.
- Every configured kernel, platform rootfs, and container rootfs image is a
  readable, non-empty regular file.
- When workspace storage is configured, `workspace.root` is the active trusted
  host JuiceFS mount and `workspace.exportfs_bin` and
  `workspace.mountpoint_bin` are trusted executables. The orchestrator and
  guests do not receive the JuiceFS metadata URL.
- The configured runtime directory already exists and is writable by the
  orchestrator.
- The state database's parent directory exists and is writable.
- The configured private IPv4 pool does not conflict with host, VPN, container,
  or datacenter routing.

fire-fuse checks these conditions where practical but does not enable IPv4
forwarding, install binaries, create boot images, or choose a collision-free
site network on the operator's behalf.

The Ansible staging role performs the site-specific selection check before it
renders a release. It compares the proposed pool with all nondefault host IPv4
routes and fails on any overlap. Existing routes are ignored only when the
interface's deterministic name and alias positively identify a fire-fuse-owned
TAP, allowing a release to be staged while managed VMs use the same pool.

Workspace storage also has an external infrastructure prerequisite. Provision
and format the JuiceFS volume, mount it once on the host, and configure the
locked-down NFSv4 gateway before enabling it in fire-fuse. fire-fuse manages
only per-workspace directories, exact exports, and guest-IP firewall
allowances; it does not deploy or manage the metadata database, object store,
backups, credential rotation, capacity, or availability. Redis and MinIO need
accept traffic only from the host gateway, not from guest subnets.

The dedicated storage host has the private `wg-storage` address
`10.78.0.2/32`; compute uses `10.78.0.1/32`. Redis and MinIO bind only to
the storage peer address, and the host JuiceFS client reaches them through the
private tunnel. Guests reach their workspace over NFS from the compute host.

Run backend provisioning, backup destination, capture, transfer, audit, restore
rehearsal, and credential rotation playbooks against `storage_hosts`. Backup
capture and credential rotation delegate the drain check and compute service
lifecycle to `fire_fuse_hosts`; both hosts must be reachable from Ansible.
A backup capture stops compute's JuiceFS/NFS services before snapshotting
Redis and MinIO. The storage host needs its own disk monitoring, off-host
backup destination, SSH maintenance access, and UDP 51821 firewall rule.

For a compute-host replacement, point the single `fire_fuse_hosts` inventory
entry at the new machine, update the two WireGuard peer keys, and provision the
new host's JuiceFS client, cache, NFS gateway, and runtime. The storage volume
and its backup history stay on the storage machine. Fence the previous compute
peer before assigning `10.78.0.1` to the replacement. The completed one-way move
off the compute host is recorded in
[history/storage-host-cutover.md](history/storage-host-cutover.md).

The staging Redis service binds only to loopback and the private storage
address. Its password and the derived JuiceFS metadata URL are root-only files
under `/etc/fire-fuse`; `/etc/redis/fire-fuse.conf` is root-owned and readable
by the `redis` service group. Redis uses database 1 exclusively for the current
volume, AOF persistence with an `everysec` policy, periodic RDB snapshots, a
configured memory ceiling, and `maxmemory-policy noeviction`. Reaching the
memory ceiling therefore rejects writes instead of evicting filesystem
metadata. Monitor capacity well before that point.

This single Redis instance is not highly available. Copying its live files is
not a complete backup procedure. Define and test both Redis persistence backup
and JuiceFS metadata dump/restore before treating the workspace as production
storage.

The initial policy requires a quiesced explicit JuiceFS metadata dump and the
matching MinIO object snapshot to be retained as one off-host backup set. Redis
AOF/RDB files remain a secondary engine-specific recovery artifact. Do not
enable retention deletion until a complete isolated restore has succeeded.

The Ansible `juicefs_volume` role owns initial volume formatting. It checks the
selected Redis database size before making any change: an empty database may be
formatted once, while any nonempty database must pass `juicefs status` with the
configured volume name, S3 storage type, and MinIO bucket. Never clear database
1 to force the role to rerun. That destroys the only metadata mapping between
workspace paths and MinIO objects.

## JuiceFS client upgrades

Treat the host client and the client baked into the guest rootfs as one release
unit. The host binary performs status, smoke, rotation, and recovery
operations; every workspace VM uses the guest binary. Allowing those pins to
drift makes host qualification weaker than the runtime it is intended to
qualify.

The upstream [JuiceFS upgrade
guide](https://juicefs.com/docs/community/administration/upgrade/) requires a
normal mount to be unmounted and remounted to use the replacement binary, with
applications stopped before unmount.

Do not use JuiceFS smooth upgrade for this deployment. Workspace mounts run in
the foreground under guest systemd and the guest rootfs is immutable. Drain and
replace the VMs so the normal boot, mount-readiness, unmount, and recovery paths
are exercised with the new client. Never overwrite a client binary that still
owns an active mount.

### Prepare an upgrade

1. Read the complete JuiceFS release notes between the current and proposed
   versions. Treat a major-version change, metadata-format change, changed
   default, or required volume configuration mutation as a separate design
   review.
2. Confirm the current off-host backup set and isolated restore evidence meet
   the backup contract. Do not combine an unqualified client upgrade with the
   first backup or restore experiment.
3. Record the current host JuiceFS version, artifact SHA-256 values,
   `juicefs status`, active configuration, and observability baseline.
4. Update every version and checksum pin in one reviewed commit:
   - `storage/juicefs/prepare-juicefs-linux-amd64.sh`;
   - `juicefs_volume_release`;
   - `juicefs_smoke_release`; and
   - the controller artifact path and checksum in Ansible inventory.
5. Build the checksum-verified host client. Do not download an unpinned client
   on the production host.

Changing the client does not authorize `juicefs config`, feature enablement,
trash-policy changes, credential rotation, metadata loading, or volume
reformatting. Make each of those a different maintenance operation. In
particular, do not raise `--min-client-version` until the rollback window has
closed and every permitted client has been upgraded.

### Apply and qualify

1. Stop new lifecycle requests, gracefully terminate every VM, and require the
   API inventory to contain no `starting`, `running`, or `stopping` record.
2. Stop `fire-fuse.service` and confirm that no host JuiceFS mount remains.
3. Apply `juicefs-volume.yml` on the storage host with unchanged Redis and MinIO credentials.
   It must install the reviewed host artifact, report the proposed version, and
   accept the existing nonempty volume without formatting it.
4. Run `juicefs-smoke.yml`. Require write, fsync, unmount, remount, exact read,
   marker deletion, and final unmount to succeed with the new host client.
5. Stage and promote the release containing the new rootfs, then activate
   fire-fuse and require `/readyz`.
6. Run the normal runtime qualification before the disruptive failure cases.
   Confirm a newly created guest reports the proposed `juicefs version`, the
   JuiceFS metrics endpoint is scrapeable, and no object-request errors appear.
7. Run the hung-unmount, Firecracker-crash, and orchestrator recovery
   qualifications before accepting the upgrade. Validate a storage outage
   separately against the dedicated storage host.
8. Retain the prior host artifact, guest rootfs, release manifest, and
   configuration until the rollback window closes.

### Roll back

Rollback is allowed only when the reviewed release notes say the prior client
can safely access the unchanged volume configuration. Drain and stop
fire-fuse, restore the previous pinned host artifact and guest release through
the same Ansible paths, rerun the host smoke test, reactivate, and rerun normal
runtime qualification.

Do not roll back a binary after enabling a feature or minimum-client constraint
that excludes it. If compatibility is uncertain, keep fire-fuse stopped and
restore into isolated Redis and MinIO targets from the last qualified backup;
never test downgrade compatibility against the production metadata database.

## Redis metadata maintenance

Treat every Redis package upgrade or configuration change that restarts the
service as an offline storage operation. Redis is the authoritative JuiceFS
namespace, so a short process restart is not equivalent to an ordinary
fire-fuse restart while guests still have the volume mounted.

The Ansible `redis_metadata` role previews its managed Redis configuration and
systemd unit before writing either file. If they would change, the role refuses
to continue while `fire-fuse.service` is running. This guard prevents an
Ansible-managed restart from interrupting active guests; it does not drain VMs,
protect package upgrades performed outside Ansible, or replace a backup.

For planned maintenance:

1. Stop new lifecycle requests, gracefully terminate every VM, and require no
   `starting`, `running`, or `stopping` record.
2. Confirm that the latest complete off-host backup and isolated restore
   evidence satisfy the backup contract.
3. Stop `fire-fuse.service`, require it to be inactive, and confirm that no
   host JuiceFS mount remains.
4. Record the Redis version, persistence status, memory use, and current
   storage-observability event. Require the latest AOF write and RDB save
   statuses to be healthy before proceeding.
5. With fire-fuse still stopped, preview and then apply only the Redis
   playbook:

   ```sh
   ansible-playbook \
     --inventory inventory/staging/hosts.yml \
     --ask-vault-pass \
     --check \
     --diff \
     playbooks/storage/redis.yml

   ansible-playbook \
     --inventory inventory/staging/hosts.yml \
     --ask-vault-pass \
     playbooks/storage/redis.yml
   ```

6. Require `fire-fuse-redis.service` to be active and enabled, run
   `fire-fuse-observe-redis.service`, and inspect its structured journal event.
7. Run `juicefs status` and the host `juicefs-smoke.yml` qualification before
   restarting the orchestrator.
8. Start fire-fuse, require `/readyz`, and run the normal runtime qualification
   before reopening lifecycle requests.

The ordinary Redis playbook must not be used to rotate its password. Use the
dedicated credential-rotation playbook, which couples the Redis password and
JuiceFS metadata URL and provides rollback.

If Redis does not return healthy after maintenance, keep fire-fuse stopped and
preserve the data directory and journal evidence. Restore the previous package
or managed configuration only when its on-disk compatibility is established.
Do not run `FLUSHDB`, delete AOF/RDB files, clear database 1, or load a metadata
dump over the live database to make startup succeed. A suspected data recovery
must follow the isolated restore contract before any production cutover.

## MinIO object-store maintenance

Treat every MinIO server upgrade or environment or systemd change that restarts
the service as an offline storage operation. The current single-node,
single-drive deployment has no second MinIO server to serve JuiceFS objects
during a restart.

The Ansible `minio_object` role previews its verified server binary, secret
environment, and systemd unit before writing any of them. If they would change,
the role refuses to continue while `fire-fuse.service` is running. Updating the
standalone `mc` client does not restart MinIO, but its installation is also
deferred until after this guard so a rejected maintenance run cannot partially
apply the proposed toolset.

For planned maintenance:

1. Stop new lifecycle requests, gracefully terminate every VM, and require no
   `starting`, `running`, or `stopping` record.
2. Confirm that the latest complete off-host backup and isolated restore
   evidence satisfy the backup contract.
3. Stop `fire-fuse.service`, require it to be inactive, and confirm that no
   host JuiceFS mount remains.
4. Record the MinIO server and `mc` versions, artifact SHA-256 values, readiness
   result, filesystem capacity, and current storage-observability event. Review
   every intervening MinIO release note before changing the server pin.
5. Build or acquire the reviewed checksum-pinned artifacts on the controller
   and update their inventory paths and checksums. With fire-fuse still
   stopped, preview and then apply only the MinIO playbook:

   ```sh
   ansible-playbook \
     --inventory inventory/staging/hosts.yml \
     --ask-vault-pass \
     --check \
     --diff \
     playbooks/storage/minio.yml

   ansible-playbook \
     --inventory inventory/staging/hosts.yml \
     --ask-vault-pass \
     playbooks/storage/minio.yml
   ```

6. Require `fire-fuse-minio.service` to be active and enabled, require the
   private readiness endpoint, run `fire-fuse-observe-minio.service`, and
   inspect its structured journal event. The role must also authenticate and
   reconcile the expected bucket and dedicated JuiceFS user.
7. Run `juicefs status` and the host `juicefs-smoke.yml` qualification before
   restarting the orchestrator.
8. Start fire-fuse, require `/readyz`, and run the normal runtime qualification
   before reopening lifecycle requests.

Keep the root and JuiceFS MinIO credentials unchanged during ordinary
maintenance. Rotate the JuiceFS credential only through its dedicated playbook,
which updates the secret stored in JuiceFS metadata and provides rollback. A
root-admin credential change needs its own reviewed maintenance and rollback
procedure; changing only its Vault value is not part of a server upgrade.

If MinIO does not return healthy after maintenance, keep fire-fuse stopped and
preserve the data directory, installed artifact, and journal evidence. Restore
the prior binary or managed files only when the reviewed release notes confirm
on-disk compatibility. Do not delete `.minio.sys`, remove unfamiliar bucket
objects, mirror an empty source over the bucket, or recreate the bucket to make
readiness succeed. Suspected object recovery must follow the isolated restore
contract before any production cutover.

## Object-store lifecycle policy

### Workspace deletion

A console Workspace delete is asynchronous and idempotent. The product record
is archived first so no new traffic or Sessions are admitted. The control
worker then asks Fire Fuse to terminate any remaining sandboxes, quarantine
the exact canonical Workspace directory as a mount-root
`.purging-<workspace-id>` entry, and run the pinned
`juicefs rmr --skip-trash` command through the root-only, unexported
administrative mount. The guest-safe data mount keeps JuiceFS management nodes
disabled. A retry resumes the quarantined directory instead of widening the
deletion target. Fire Fuse removes Sessions,
leases, encrypted Messages, deliveries, executions, request events, and
terminated sandbox records only after filesystem removal succeeds; it retains
the archived Workspace row as an audit tombstone.

Do not manually remove Workspace paths from the host mount or objects from
MinIO. A completed Workspace purge removes the live filesystem namespace, but
unreferenced JuiceFS blocks can remain in the object store because production
mounts disable background jobs. Reclaim those blocks only through the drained,
backup-qualified garbage-collection procedure below.

Do not configure MinIO expiration, transition, or noncurrent-version deletion
rules on the live `fire-fuse-workspaces` bucket. JuiceFS splits files into
opaque blocks and keeps their path, slice, and reference relationships in
Redis. MinIO object age does not indicate whether a block is still referenced,
so an independent bucket rule can silently delete live filesystem data.

JuiceFS must own deletion from the live bucket. The volume's `TrashDays`
setting, pending deletions, trash files, and stale slices are metadata-governed
state; inspect them with the pinned client:

```sh
juicefs status META-URL --more
```

The guest and host smoke mounts intentionally use `--no-bgjob`, which disables
the normal client jobs that clean expired trash and unreferenced slices.
Object-store usage can therefore remain above visible filesystem usage and grow
after deletes or overwrites. Treat that difference as capacity to investigate,
not permission to add a MinIO expiration rule.

Use this conservative reclamation process:

1. Alert on MinIO filesystem capacity early enough to preserve a maintenance
   window; do not wait for the backing filesystem to fill.
2. Record `juicefs status --more`, MinIO capacity, and the age and size of the
   latest complete backup set.
3. Run `juicefs gc META-URL` without `--delete` or `--compact` first. Preserve
   its valid, leaked, and skipped object counts as evidence.
4. Investigate unexpected leaked objects and recent skipped objects before
   authorizing mutation. A large bucket alone is not proof of a leak.
5. Treat `juicefs gc --delete`, `juicefs gc --compact`, trash deletion, or a
   `TrashDays` change as a separate destructive maintenance operation. Drain
   all clients, stop fire-fuse, create and verify a complete backup set, record
   the exact command and pinned client version, then rerun the scan and host
   smoke qualification afterward.

Never run `juicefs destroy` against the production metadata URL or volume UUID.
It deletes both metadata and associated object data and is not a capacity
management command.

Lifecycle expiration is allowed only on the off-host backup destination and
must operate on complete backup sets through their manifests. Apply the
retention schedule only after an isolated restore succeeds, never expire the
last known-good set, and never apply backup-destination rules to the live
MinIO bucket. The detailed retention and restore contract is in
[storage-backup.md](storage-backup.md).

These constraints follow the upstream JuiceFS
[storage architecture](https://juicefs.com/docs/community/architecture/),
[trash behavior](https://juicefs.com/docs/community/security/trash/), and
[garbage-collection procedure](https://juicefs.com/docs/community/administration/status_check_and_maintenance/).

## Stale qualification-marker cleanup

Successful storage qualifications remove their own marker. A marker left at
the volume root is therefore evidence of an interrupted or failed run, not an
age-based cleanup candidate. The current reserved names are:

- `.fire-fuse-host-smoke`;
- `.fire-fuse-integration-vm-<32 lowercase hexadecimal characters>`;
- `.fire-fuse-minio-outage-vm-<32 lowercase hexadecimal characters>`; and
- `.fire-fuse-reboot-vm-<32 lowercase hexadecimal characters>`.

Never schedule wildcard deletion for `.fire-fuse-*`. That prefix is not an
ownership proof, file modification time is not an ownership proof, and a
future qualification may add another reserved name with different semantics.

After a qualification reports incomplete cleanup:

1. Preserve its complete output and record the test type, time, and every VM ID
   it created.
2. Require no qualification process to remain active. Query the lifecycle API
   and terminate any test VM that is still `starting`, `running`, or `stopping`
   before touching its marker.
3. Start one temporary workspace VM solely to inspect the shared volume, and
   ensure `/workspace` is a JuiceFS mount before continuing.
4. Inventory candidates without changing them:

   ```sh
   find /workspace -xdev -maxdepth 1 -type f \
     \( -name '.fire-fuse-host-smoke' \
     -o -name '.fire-fuse-integration-vm-*' \
     -o -name '.fire-fuse-minio-outage-vm-*' \
     -o -name '.fire-fuse-reboot-vm-*' \) \
     -printf '%f\t%s bytes\t%TY-%Tm-%TdT%TH:%TM:%TS\\n'
   ```

5. For every candidate, require an exact reserved-name match and correlate its
   VM suffix with the failed run's output or a retained terminal lifecycle
   record. Inspect the content as evidence: the integration and MinIO-outage
   tests use fixed identifying text, the reboot test uses its configured marker
   value, and the host smoke marker records host, JuiceFS version, and UTC time.
6. Remove only the fully spelled absolute path of each confirmed marker—never
   pass a glob to `rm`. Confirm that the exact path no longer exists and retain
   the deletion record with the failed qualification output.
7. Gracefully terminate the temporary inspection VM and require its lifecycle
   record to reach `terminated`.
8. Rerun the qualification that left the marker. Require successful marker and
   VM cleanup before closing the incident.

If a candidate cannot be correlated or its contents do not match the expected
test, leave it in place and investigate it as user data. Do not use direct
MinIO object deletion: marker removal must pass through JuiceFS so Redis
metadata and object references remain consistent.

This procedure assumes the current single shared volume root. Before adding a
multi-tenant workspace boundary, move qualification artifacts into a dedicated
administrative namespace and revise cleanup authorization so one tenant can
never inspect or remove another tenant's paths.

## Disaster-recovery exercise

The recovery objective is a usable copy of the shared JuiceFS volume on a
replacement host. It does not preserve running microVMs, guest memory, network
connections, or fire-fuse lifecycle continuity. Recovery starts new VMs against
restored storage.

Run every exercise in a network and credential scope that cannot reach or
modify the production Redis database or MinIO bucket. Use a new empty Redis
database and a new empty MinIO bucket whose names include the exercise ID.
Never rehearse by loading over production or by temporarily repointing active
clients.

### Prepare the exercise

1. Select one complete off-host backup-set manifest and record its backup time,
   source volume name and UUID, component versions, and storage snapshot
   identity.
2. Define the acceptance sample before restoration: representative small and
   large files, directory structure, permissions, and content checksums. Include
   at least one artifact written before the selected backup's timestamp.
3. Provision an isolated recovery host with the same pinned JuiceFS client and
   reviewed Redis and MinIO versions. Give the restored services distinct
   credentials from production.
4. Record the exercise start time, target recovery-time objective, selected
   recovery point, operators, and explicit evidence directory.

### Restore and qualify

1. Verify every manifest checksum before using the backup.
2. Restore the object snapshot into the empty recovery bucket.
3. Load the JuiceFS binary metadata dump into the empty recovery Redis
   database. Reapply the recovery bucket endpoint and dedicated credential from
   the encrypted operations secret store; never copy secrets into the evidence
   log.
4. Restore the Redis AOF/RDB artifact into a second isolated Redis instance and
   validate that engine-level recovery independently. Do not combine that
   database with the one populated by the JuiceFS metadata dump.
5. Run `juicefs status` against the dump-restored database and require the
   expected volume name, UUID, storage type, and recovery bucket.
6. Mount the recovered volume read-only first. Verify every predefined sample,
   unmount it, remount it, and repeat the checks.
7. Mount it read-write in the isolated environment and run the host
   write/fsync/unmount/remount/read/delete smoke qualification. Confirm that no
   production endpoint received a request.
8. Install fire-fuse on the isolated replacement host with a fresh lifecycle
   state database and the recovered metadata URL. Require `/readyz`, then run
   the normal two-concurrent-VM and new-VM persistence qualification.
9. Run the selected storage-outage, process-crash, and reboot qualifications
   before declaring the recovered environment operable.
10. Record total restore time, bytes and objects restored, validation duration,
    errors, manual interventions, newest recovered timestamp, and measured data
    loss relative to the exercise start.

### Decide and close

An exercise passes only when all checksums match, the host and microVM
qualifications succeed, cleanup completes, and the measured recovery point and
duration meet the stated targets. A mount that merely starts is not a
successful recovery.

Do not switch production clients during a rehearsal. A real cutover requires a
separate incident decision: fence the old Redis and MinIO endpoints first,
prove that no old client can write, then expose exactly one recovered writable
volume. Never attempt to merge two independently writable Redis databases or
MinIO buckets after they diverge.

Retain the manifest, commands with secrets redacted, service logs,
qualification output, measurements, and pass/fail decision. Keep the isolated
targets until the evidence is reviewed; then destroy them using their exact
exercise identifiers. Do not apply production retention rules to recovery
targets before review is complete.

Perform this full exercise at least quarterly and after changing JuiceFS,
Redis, MinIO, backup tooling, credentials, or retention policy. The detailed
backup contents, retention schedule, and restore invariants are defined in
[storage-backup.md](storage-backup.md).

Ansible stages a proposed fire-fuse runtime release under
`/var/lib/fire-fuse/staged-release` before touching live paths. The staged
`release.json` is the promotion allowlist: it records each verified artifact's
SHA-256, destination, and mode plus the proposed configuration destination.
Staging alone does not authorize manual promotion or a service restart.

The guest kernel must support Linux `ip=` boot-time autoconfiguration for the
injected static network argument. The platform image's resolver configuration
is bind-mounted into the container rootfs.

## Suggested filesystem layout

The example configuration uses this layout:

```text
/usr/local/bin/fire-fuse
/usr/local/bin/firecracker
/usr/local/bin/jailer
/etc/fire-fuse/config.json
/etc/fire-fuse/image-catalog.json
/etc/fire-fuse/control-clients/operator.token
/etc/fire-fuse/control-clients/gateway.token
/etc/fire-fuse/control-clients/dispatcher.token
/etc/fire-fuse/control-clients/observer.token
/etc/fire-fuse/control-clients/compute.token
/etc/fire-fuse/artifact-store.env
/etc/fire-fuse/juicefs-meta-url
/var/lib/fire-fuse/state.db
/var/lib/fire-fuse/state.db.lock
/var/lib/fire-fuse/images/vmlinux-6.1
/var/lib/fire-fuse/images/ubuntu-24.ext4
/var/lib/fire-fuse/images/sha256-<rootfs-digest>.ext4
/var/lib/fire-fuse/firecracker/<vm-id>/
```

The final jail directory name includes the basename of `firecracker_bin`; it is
therefore `firecracker` in this example. `state.db.lock` is created and retained
by fire-fuse with mode `0600`. The database itself is also forced to `0600`.

A conservative manual setup is:

```sh
sudo install -d -m 0750 /etc/fire-fuse
sudo install -d -o root -g root -m 0700 /etc/fire-fuse/control-clients
sudo install -d -m 0751 /var/lib/fire-fuse
sudo install -d -m 0750 /var/lib/fire-fuse/images
sudo install -m 0755 ./fire-fuse /usr/local/bin/fire-fuse
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
sudo install -m 0600 ./juicefs-meta-url /etc/fire-fuse/juicefs-meta-url
```

Install Firecracker, jailer, and boot artifacts separately at the absolute
paths selected in configuration. Treat both configured source root filesystems
as immutable. fire-fuse bind-mounts the kernel and both root filesystems
read-only into each jail. The guest constructs separate private ephemeral
OverlayFS upper layers for the platform and container roots. See
[docker-images.md](docker-images.md) for producing the container artifact and
resolved process configuration from a Docker image.

## Configure the host

Start from `config.example.json` and `image-catalog.example.json`, and review
every field. Both documents are decoded strictly: unknown fields, relative
paths, unclean paths, invalid durations, and missing required values stop
startup.

Important checks include:

- The configuration path is clean and absolute. Its configured and resolved
  parents are trusted, and it resolves to a root-owned regular file that is not
  group- or world-writable.
- `jailer_uid` and `jailer_gid` are nonzero and reserved for jailed runtimes.
- `runtime_dir` names the existing runtime hierarchy.
- `state_db` names a file, not a directory.
- An existing `state_db` is a regular file owned by the effective service UID;
  final symlinks and special files are rejected, and its mode is set to `0600`.
- Firecracker, jailer, `ip`, and `iptables` resolve to root-owned regular files
  that are not group- or world-writable; their configured and resolved parent
  hierarchies satisfy the same directory policy.
- Every profile's kernel, platform rootfs, and container rootfs image satisfies
  the same root-owned regular-file and trusted-parent policy before it is
  prepared in a VM jail. Each artifact must also be readable by the jail
  identity because its read-only bind mount retains the source file ownership.
- Every profile defines a non-empty image command, environment entries in
  `NAME=value` form, a clean absolute working directory, and numeric UID/GID.
- The configured and resolved `runtime_dir`, the `state_db` parent, and every
  ancestor back to `/` are owned by root and are not group- or world-writable.
  Root-controlled symlinks are allowed; preflight validates both the path that
  controls each symlink and its resolved directory hierarchy.
- `network.address_pool` is a canonical private IPv4 prefix containing enough
  `/30` subnets for `limits.max_vms`. Fixed host-pool capacity is included in
  that ceiling rather than added on top of it.
- `host_pool.slots_per_profile` is zero to disable fixed warm capacity and use
  synchronous preparation, or a positive sandbox capacity for every snapshot
  profile. Across all profiles the configured capacity cannot exceed
  `limits.max_vms`. Each unit is either ready, claimed by one sandbox, or being
  prepared; a claim is replenished only after that sandbox is cleaned during
  termination. Ready units reserve addresses and TAP/firewall state, but no
  vCPU, guest memory, container image, workspace, or Firecracker process.
  The staging host inventory explicitly sets 50 slots; `max_concurrent_starts`
  remains 2 and only limits simultaneous startup work. A pool of 2 slots would
  cap the host at 2 live sandboxes, including idle sessions in keepalive.
  Admission checks available profile capacity before recording a new sandbox.
  A full pool returns HTTP 429 instead of creating a failed sandbox per retry.
- Unbound message admission retries use persisted attempt and update timestamps
  for delays of 1, 2, 4, then at most 8 seconds. They expire after 30 seconds
  with `session_admission_timeout`, before attempting another session start.
  Restarting the dispatcher does not reset the window. Later messages for the
  same logical session wait behind a delayed message; other sessions can run.
  This admission deadline is separate from mailbox ACK and handler deadlines.
- The optional workspace paths name trusted `exportfs` and `mountpoint`
  executables plus a root-controlled directory below `/var/lib/fire-fuse`.
  The root must be an active mount before an export can be created.
- Enabling `workspace` exports exactly
  `<workspace.root>/<workspace_id>` to the assigned guest IP. Equal IDs share
  one directory; different IDs have distinct NFS export boundaries.
- VM, total-guest-memory, and concurrent-start limits reflect real host
  capacity. Sandbox cgroups apply equal CPU weight and a four-core ceiling;
  guest vCPUs do not reserve host cores. See the capacity qualification in
  [Sandbox CPU and memory](sandbox-resources.md#capacity-qualification) before
  raising live capacity.
- All timeout values are positive duration strings such as `"5s"`.
- `timeouts.guest_shutdown` allows workspace-enabled guests to stop services
  and unmount NFS before host-side process termination begins.
- `timeouts.http_request_read` bounds headers and request bodies;
  `timeouts.http_idle` bounds inactive keep-alive connections. HTTP writes are
  intentionally governed by lifecycle deadlines instead of a shorter generic
  server deadline.
- Profile names and boot-artifact paths are operator-controlled.
- Profile `boot_args` do not contain `ip=` or `fire-fuse.workspace=`;
  fire-fuse owns those arguments.

Enabling IPv4 forwarding is a host-wide policy decision. Configure it through
the operating system's persistent sysctl mechanism and confirm it before
starting fire-fuse:

```sh
sysctl net.ipv4.ip_forward
```

The reported value must be `1`.

## Start and verify

Before starting or restarting a newly promoted binary, run its bounded
configuration and host check:

```sh
sudo /usr/local/bin/fire-fuse \
  -config /etc/fire-fuse/config.json \
  -preflight-only
```

This mode validates the numeric loopback listen address, loads the trusted
configuration, and runs the normal host preflight contract. It exits without
opening the lifecycle database, reconciling runtimes, inventorying orphans,
starting Firecracker, or opening the HTTP listener. The runtime-directory
preflight still creates, changes ownership of, and removes one temporary probe
file. A zero exit is necessary before activation but does not replace startup
recovery and orphan checks.

Run the current manual deployment with:

```sh
sudo /usr/local/bin/fire-fuse \
  -config /etc/fire-fuse/config.json \
  -listen 127.0.0.1:8080
```

Startup is deliberately fail-closed and proceeds in this order:

1. Validate the loopback listen address and trusted configuration file, then
   strictly decode its contents.
2. Run host preflight checks.
3. Construct the supervisor and exclusively lock the SQLite state store.
4. Reconcile all interrupted VM and host-slot ownership.
5. Inventory jails, TAPs, and tagged firewall rules for orphans.
6. Prepare the configured number of reusable slots for every snapshot profile.
7. Open the HTTP listener only after the initial pool is full.

The listener is unavailable during recovery. A recovery, inventory, database,
or preflight failure exits the process instead of serving partially.

Every restored VM receives its resolved image process configuration through the
host-to-guest vsock assignment protocol. The host waits for ordered acceptance,
clock, network, container mount, optional workspace mount, and workload-started
events before publishing the runtime as ready.

For workspace-enabled VMs, the same assignment includes the workspace ID, host
gateway address, and exact NFS export path. The guest writes those values to
mode-`0600` files under `/run/fire-fuse/workspace`, then mounts that export at
`/workspace` with NFSv4. The JuiceFS client runs only on the host: Redis and
MinIO credentials and the JuiceFS metadata URL never enter the guest or the
fire-fuse configuration.

The host uses NFSv4.1/4.2 directly on TCP port 2049 and does not need the
Portmapper service. The workspace-gateway deployment masks both rpcbind units,
requires them to remain inactive, and drops TCP and UDP port 111 for IPv4 and
IPv6 before the NFS package is installed. A higher-priority loopback allowance
lets local NFS helpers fail their Portmapper probe immediately while the masked
service has no listener; public and guest traffic still reaches the deny rule.
Verify that boundary after deployment or a package upgrade:

```sh
sudo systemctl show \
  --property=LoadState \
  --property=ActiveState \
  rpcbind.service rpcbind.socket
sudo ss --no-header --listening --tcp --udp --numeric 'sport = :111'
```

Both units must report `LoadState=masked` and `ActiveState=inactive`. The `ss`
command must produce no output. Also test the server's public IPv4 and IPv6
addresses from outside the Hetzner network; neither TCP nor UDP port 111 may
respond.

Before boot, fire-fuse creates
`<workspace.root>/<workspace_id>`, exports exactly that directory to the
assigned guest IPv4 address, and installs an NFS firewall allowance matching
both the VM's TAP device and source address. VMs using the same workspace ID
receive the same export; different IDs receive different export roots. The
guest-visible workspace ID is a storage selector, not a tenant credential:
authorization between a caller and a workspace ID remains outside this
loopback-only API.

The host delivers the workspace ID, gateway IP, and export path in the
restored guest's vsock assignment. The guest assignment agent waits for an IPv4
route on `eth0`, mounts the export at `/workspace`, and confirms it with
`mountpoint` before it starts the workload and reports the `workspace_mounted`
milestone. The VM is recorded as `running` only after the assignment succeeds
within `timeouts.startup`. Workspace-disabled assignments skip these steps. A
timeout, mount failure, rejected assignment, or early Firecracker exit triggers
normal startup rollback.

Snapshot-assigned SDK workloads use a platform-owned socket bridge rather than
TCP readiness polling. As soon as the guest network is configured, the bridge
owns the App's private gateway address and queues its first connection while
Bun starts on a platform-selected loopback port. Once the SDK listener exists,
the bridge forwards the complete byte stream in both directions. The assignment
agent owns the bridge for the lifetime of the VM, so this path is identical for
every customer and does not require customer-specific snapshots or pools.

The workspace mount starts as soon as the assignment agent has configured
`eth0` and the kernel can route IPv4 through it, without waiting for the
broader `network-online.target`. DNS remains available from the static resolver
configuration baked into the guest image.

Every successful VM start emits one structured `microVM startup completed`
event. Its microsecond fields separate jail preparation, host network setup,
Firecracker launch, snapshot restore, and guest assignment (including workspace
mount), plus the total. It includes the Sandbox ID and whether workspace readiness was required,
but never the workspace ID or JuiceFS metadata URL.

The durable Sandbox record also stores one product-facing startup duration
when it first enters `running`. It measures from durable Sandbox creation
through durable activation, remains unchanged during stopping or termination,
and is exposed by the lifecycle API and read-only observer. This value is
intended for inventory displays; the request-scoped trace below remains the
source for detailed startup analysis.

The guest assignment agent reports ordered milestones over vsock for clock
synchronization, network configuration, container-rootfs mount, workspace
mount, workload start, and SDK listener readiness. The host records the wait
for each as a `guest.assignment.wait_<event>` step in the request-scoped trace.
The detailed trace never includes the workspace ID, paths, process arguments,
environment values, or metadata contents.

After startup, check readiness:

```sh
curl --fail http://127.0.0.1:8080/readyz
```

`healthz` requires the private control bearer token; query it with:

```sh
curl --fail \
  --header "Authorization: Bearer $(sudo cat /etc/fire-fuse/control-clients/operator.token)" \
  http://127.0.0.1:8080/healthz
```

`healthz` only proves that the authenticated HTTP process is responding.
`readyz` deliberately remains available to a direct loopback health probe and
reruns host preflight, including a create/chown/remove probe in `runtime_dir`.
It is the stronger signal that host prerequisites for lifecycle work remain
available.

The host readiness endpoint does not write into JuiceFS. Before production use,
run a data-path qualification from the intended guest image:

1. Start two workspace-enabled VMs with different workspace IDs.
2. Write and `fsync` a unique marker in each VM and prove neither marker is
   visible from the other workspace.
3. From a running guest, require NFS to reject the other workspace's exact
   export. Mount the NFSv4 pseudo-root and require the other workspace's known
   marker to remain unreadable through it. Attempt to assign the other guest's
   address as an additional source address. Require either the guest capability
   boundary to reject source spoofing or, if the image permits it, the
   TAP-and-source firewall boundary to reject the spoofed mount.
4. Terminate the second VM, then start a VM with the first workspace ID and
   read the first marker while the original VM remains running.
5. Gracefully terminate both VMs.
6. Start a new VM with the first ID, read the marker again, and then remove it.

The Hetzner staging bundle automates this sequence when workspace configuration,
`FIRE_FUSE_INTEGRATION_BUILD_ID`, and `FIRE_FUSE_INTEGRATION_SSH_KEY` are
present. The Build ID selects one exact active catalog entry, while the VM shape
is the default profile's 4-vCPU/512-MiB boot snapshot with a 4096-MiB hotplug range. A successful run demonstrates
same-ID sharing, different-ID filesystem isolation, rejection of direct and
attempted source-spoofed cross-workspace mounts, pseudo-root file isolation, and
persistence across VM lifecycles; it does not demonstrate backup recovery,
performance under load, resource quotas, caller-to-workspace authorization, or
a complete multi-tenant security boundary. The optional disruptive cases
separately qualify metadata and object store outages.

## Logs

fire-fuse writes its own structured JSON events to stdout. Request-completion
events contain `request_id`, `method`, matched `route`, `status`, `duration`, an
optional `vm_id`, and an error class for 4xx/5xx responses. Query strings,
request bodies, authorization headers, and unmatched raw paths are excluded.

The jailer and Firecracker stdout/stderr streams are also forwarded to the
service stdout. Those child-process lines are not guaranteed to be JSON. A log
collector must therefore tolerate both fire-fuse JSON events and raw runtime
output.

Use the response's `X-Request-ID` to correlate a client operation with its
completion event. Request completion events intentionally omit internal error
text; clients receive stable safe error codes and messages. Service-startup and
shutdown failure events include their internal error, while child runtime
diagnostics remain in the forwarded process output. Treat the combined log as
sensitive because guest console or runtime output may contain workload data.

## Storage observability

Storage observability remains outside the orchestrator. The Ansible
`storage_observability` role installs hardened one-shot probes and systemd
timers. Redis and MinIO observation is enabled by `storage.yml`; host JuiceFS
gateway observation is enabled by `activate-runtime.yml`.

Inspect the timer schedule and raw JSON events with:

```sh
systemctl list-timers 'fire-fuse-observe-*'
journalctl -t fire-fuse-storage-observer -o cat
```

The storage observers and daemon expose these event families:

| Event | Scope | Important fields |
| --- | --- | --- |
| `backend_health`, `component=redis` | Redis metadata service | `available`, `latency_us`, `used_memory_bytes`, `maxmemory_bytes`, `rejected_connections_total`, AOF/RDB status |
| `backend_health`, `component=minio` | MinIO readiness and its backing filesystem | `available`, `latency_us`, `filesystem_total_bytes`, `filesystem_used_bytes`, `filesystem_available_bytes` |
| `gateway_metrics`, `component=juicefs` | Single host JuiceFS client | `available`, `cache_bytes`, cache hit/miss totals, `object_request_errors_total`, optional `reason` |
| `workspace lifecycle event` | Orchestrator-observed guest lifecycle | `sandbox_id`, `event`, `outcome`, `duration_us`, optional `error` |

Successful Redis and MinIO probes are constant-cost. MinIO byte fields describe
the complete filesystem containing its data directory, not logical S3 bucket
usage. The observer deliberately avoids a recurring object-tree scan.

The JuiceFS observer scrapes the gateway's loopback-only metrics endpoint.
`available=false,reason=scrape` means the single host client could not be
observed and should be correlated with the gateway service and backend health.

Counters such as rejected connections, cache hits and misses, and object
request errors are cumulative. A monitoring system must retain samples and
calculate rates or deltas. At minimum, alert on:

- missing probe events for more than two configured intervals;
- repeated `available=false` events;
- unhealthy Redis AOF or RDB status;
- Redis memory or MinIO filesystem headroom crossing an approved capacity
  policy;
- increasing JuiceFS object-request errors; and
- repeated forced workspace shutdowns or readiness failures.

Do not encode capacity or latency thresholds in the probes. Set them after the
representative benchmark and failure qualification establish host-specific
limits. Compare `cache_bytes` with the configured host gateway cache size;
cache effectiveness requires deltas of both hit and miss counters.

The role does not install Prometheus, a log collector, dashboards, alert
routing, or credentials for an external monitoring system. JuiceFS metrics are
unauthenticated but bind only to host loopback on port `9567`.

### Local operations dashboard

The optional Ansible `netdata_dashboard` role provides a deliberately bounded
single-host dashboard without adding behavior to the orchestrator. Apply
`platform/ansible/playbooks/shared/dashboard.yml` only after the storage observers and
runtime are active. It consumes sanitized aggregate snapshots from
`/run/fire-fuse-observability`; Redis and MinIO credentials never enter
Netdata, and the host JuiceFS snapshot contains no workspace, machine, or guest
network identity.

The custom dashboard charts:

- fire-fuse readiness and fresh Redis, MinIO, and JuiceFS observations;
- Redis and MinIO probe latency;
- Redis memory and rejected connections;
- MinIO backing-filesystem capacity;
- host JuiceFS gateway availability and cache use; and
- cache operations and object-store request errors.

Basic CPU, memory, disk, and network charts come from Netdata's restricted host
collectors. Workspace lifecycle durations and individual VM diagnostics remain
structured journal events; adding them to persistent metrics would require a
separate bounded-cardinality aggregation contract.

Netdata listens only on loopback and has no application authentication layer.
Reach it through an SSH tunnel:

```sh
ssh -N -L 19999:127.0.0.1:19999 root@fire-fuse-host
```

Then browse to `http://127.0.0.1:19999`. Do not bind port `19999` to the
private guest address or a public interface.

The role applies these resource and retention boundaries:

| Boundary | Value |
| --- | --- |
| CPU quota | 25% of one core |
| Memory high / maximum | 192 MiB / 256 MiB |
| I/O weight | 10 |
| Collection interval | 5 seconds |
| Netdata static installation | approximately 600 MiB |
| Metrics storage | one 256 MiB tier |
| Retention | 14 days or the size limit, whichever is reached first |

The storage limit is a Netdata soft retention target, so preserve additional
filesystem headroom, reserve at least 1.25 GiB total, and alert on the host
filesystem itself. Netdata data must not be placed inside the MinIO data
directory or JuiceFS.

Only curated local alerts are enabled. They appear in the dashboard but use
the `silent` recipient until an explicitly approved external notification
destination exists:

- fire-fuse not ready;
- stale or failed Redis and MinIO probes;
- an unavailable or stale JuiceFS gateway probe;
- MinIO available capacity below 20% warning or 10% critical;
- Redis maxmemory headroom below 25% warning or 10% critical;
- increasing JuiceFS object errors; and
- increasing Redis rejected connections.

Inspect the service and its enforced limits with:

```sh
systemctl status netdata.service
systemctl show netdata.service \
  --property=CPUQuotaPerSecUSec \
  --property=MemoryHigh \
  --property=MemoryMax \
  --property=IOWeight
curl --fail --silent http://127.0.0.1:19999/api/v1/info | jq .
systemctl is-enabled netdata-updater.timer
```

The updater must report `masked`. Upgrade only by changing the checksum-pinned
release in Ansible, applying the dashboard playbook, and confirming the API
reports the new version. Dashboard history shares the monitored host's failure
domain and is intentionally disposable; alerts must eventually leave the host
if host-loss notification is required.

## Administer customer tenants

Customer identity, status, and quota state are authoritative in PostgreSQL and
are not administered through Fire Fuse. The retired host commands
`cantelop list`, `cantelop set-status`, and `cantelop set-quota` are deliberately
rejected, and Fire Fuse exposes no tenant-administration routes.

There is currently no supported operator mutation interface for suspending a
user or changing persisted quota values. Do not edit PostgreSQL directly as an
operational shortcut. Add an ownership-scoped, audited product-control
operation before performing either change in production. Immediate workload
shutdown remains a separate Session/Sandbox operation and must not be inferred
from product account state.

## Stop and restart

Send SIGTERM or SIGINT for an orderly stop:

```sh
sudo kill -TERM <fire-fuse-pid>
```

fire-fuse first drains HTTP requests using `timeouts.server_shutdown`. If that
deadline expires, remaining HTTP connections are force-closed. It then uses a
separate budget derived from the guest shutdown, graceful process, forced
process, and cleanup timeouts to terminate all non-terminated VM records
concurrently.

For every tracked VM, normal termination sends Firecracker's
`SendCtrlAltDel` action and waits up to `timeouts.guest_shutdown`. On the
prepared x86 guest, systemd stops the workload and unmounts its container
rootfs; workspace-enabled guests also run `umount /workspace`. The existing
`reboot=k` kernel argument causes the reboot to terminate Firecracker. API
errors, guest hangs, and timeouts fall back to the existing SIGTERM/SIGKILL
process path. A successful fallback still permits the lifecycle record to
reach `terminated`; the graceful attempt is best-effort, while host resource
cleanup remains mandatory.

Avoid SIGKILL during normal operation. If the process crashes or is killed,
durable ownership checkpoints allow the next startup to recover, but fire-fuse
will terminate surviving VMs rather than reattach to them. Guest continuity
across an orchestrator restart is not guaranteed.

The lifecycle database records its schema in SQLite `user_version`. The current
runtime uses schema version 24, creates it on an empty database, and accepts
only an existing database at exactly version 24 with the expected structure.
Any other version, an unversioned database, or an unexpected structure fails
closed; the runtime performs no in-place migrations. A host on an older schema
must restore the release that matches its database or recreate local state and
replay desired state from PostgreSQL.

Qualification (`scripts/check-fire-fuse-state-compatibility.sh`) covers a fresh
schema-24 database, reopening it, and rejection of every other schema version.
A retained rollback snapshot restores the prior binary together with its own
database, so rollback never runs an older binary against newer state.

Before an upgrade, stop
and disable Fire Fuse cleanly, set a unique upgrade ID, and run
`prepare-runtime-upgrade.yml`. It retains the exact prior host release,
configuration, image catalog, lifecycle database, and configuration-encryption
key in a root-only create-only snapshot. Promotion requires the same upgrade ID
and verifies that the snapshot database checksum exactly matches the live
pre-upgrade database. Keep it until activation, lifecycle smoke, and the
rollback window have completed.

For an explicitly reviewed offline schema cutoff, preserve the pre-cutoff
snapshot first, transform the stopped lifecycle database, and validate the
result with the staged runtime's `-check-state-only` command. Promotion must
then bind the exact post-cutoff database checksum as a second approval gate:

```sh
export CANTELOP_FIRE_FUSE_STATE_CUTOVER='PROMOTE OFFLINE FIRE-FUSE STATE CUTOVER'
export CANTELOP_FIRE_FUSE_STATE_CUTOVER_SHA256='POST_CUTOFF_STATE_DB_SHA256'
```

The rollback manifest and its database copy remain bound to the pre-cutoff
checksum, so rollback restores the prior binary and prior schema together.
Unset both cutoff variables after promotion. Omitting either variable, using a
different confirmation, or changing the live database after review fails
closed.

If rollback is approved, stop and disable the service, select that same upgrade
ID, and run `rollback-runtime.yml`. The role verifies its strict manifest and
checksums, restores only the fixed allowlist, validates the restored host and
restored lifecycle state, and leaves Fire Fuse stopped. Run the normal activation and
qualification checkpoints afterward; rollback never starts production traffic
on its own.

Do not run two instances against the same state path. The second process fails
the non-blocking `state.db.lock` acquisition. Do not delete the lock file to
bypass a live owner; filesystem presence is not the same as holding its kernel
lock.

## Recovery and orphan troubleshooting

Recovery errors are intentionally fatal. Each cleanup release is checkpointed,
so correcting the host problem and restarting retries from the last durable
ownership state.

When startup reports a recovery failure:

1. Preserve the complete error event and the current `state.db`.
   Preserve `/etc/fire-fuse/config-encryption.key` through the protected
   operations secret boundary as well; encrypted customer secrets in the
   database cannot be recovered without that exact key.
2. Correct the reported permission, process, network, firewall, or filesystem
   problem.
3. Restart fire-fuse and let recovery retry.
4. Do not erase the database or VM directory merely to make startup proceed.

Orphan inventory is read-only. It reports reserved jail paths, deterministic
TAP names, or `fire-fuse:`-tagged firewall rules that do not match durable
ownership. Inspect the host without mutating it:

```sh
sudo ip -details link show
sudo iptables -S FORWARD
sudo iptables -t nat -S POSTROUTING
sudo find /var/lib/fire-fuse/firecracker \
  -name fire-fuse-runtime.json -print
```

Adjust the final path when `runtime_dir` or the Firecracker executable basename
differs from the example. A TAP is positively owned only when its
`fire-fuse:<vm-id>` alias derives that exact deterministic TAP name. A jail is
positively owned only when its strict, nonsymlinked runtime manifest matches the
derived VM ID. Firewall ownership requires the exact `fire-fuse:<tap-name>`
comment.

Do not delete an ambiguous resource based only on a familiar prefix. Resolve
ownership during a maintenance window, retain the state database for evidence,
and remove host resources only after independently confirming they are stale.

## Current operational limits

- One fire-fuse process manages one Linux host.
- Workspace-enabled VMs receive exact per-ID NFS exports backed by one host
  JuiceFS mount. This is a filesystem boundary, not a complete tenant boundary.
- The API is loopback-only and requires the private control bearer token,
  except for the direct loopback readiness probe.
- There is no cluster scheduling, migration, HA, or live reattachment.
- Terminated lifecycle records are retained until their Workspace is permanently deleted.
- Guest DNS and inbound service publication are outside current scope.
- No Prometheus server, dashboard, or alert router is bundled. Backend and
  gateway metrics are emitted as structured journal events for an external
  collector.
