# Ansible host provisioning

For routine software updates from the local controller, use the
[local shipping commands](../../docs/local-shipping.md). They select a component
and reuse persistent build caches; the top-level playbooks below converge hosts.

## Contents

- [Layout and deployment order](#layout-and-deployment-order)
- [Shared role and script helpers](#shared-role-and-script-helpers)
- [Provider edge firewall](#provider-edge-firewall)
- [Runtime configuration encryption key](#runtime-configuration-encryption-key)
- [Activation gateway and edge Tunnel](#activation-gateway-and-edge-tunnel)
- [Control worker on the builder VM](#control-worker-on-the-builder-vm)
- [Host baseline and storage services](#host-baseline-and-storage-services)
- [Observability and the Netdata dashboard](#observability-and-the-netdata-dashboard)
- [Runtime release overview](#runtime-release-overview)
- [Storage durability and backups](#storage-durability-and-backups)
- [Install the off-host backup destination boundary](#install-the-off-host-backup-destination-boundary)
- [Enter the backup capture consistency boundary](#enter-the-backup-capture-consistency-boundary)
- [Inventory](#inventory)
- [Apply the host baseline](#apply-the-host-baseline)
- [Configure the private storage network](#configure-the-private-storage-network)
- [Configure Redis metadata storage](#configure-redis-metadata-storage)
- [Provision MinIO object storage](#provision-minio-object-storage)
- [Qualify JuiceFS without Firecracker](#qualify-juicefs-without-firecracker)
- [Stage the fire-fuse runtime release](#stage-the-fire-fuse-runtime-release)
- [Install the inactive service gate](#install-the-inactive-service-gate)
- [Prepare a production runtime upgrade](#prepare-a-production-runtime-upgrade)
- [Promote and preflight the runtime](#promote-and-preflight-the-runtime)
- [Build and enable the runtime snapshot](#build-and-enable-the-runtime-snapshot)
- [Activate fire-fuse](#activate-fire-fuse)
- [Qualify the activated runtime](#qualify-the-activated-runtime)

## Layout and deployment order

This directory provisions and deploys the three Cantelop hosts. Roles and
playbooks are grouped by the host group they target:

```text
builder.yml  storage.yml  compute.yml   Converge one host group
playbooks/
  builder/     control_builder_hosts
  storage/     storage_hosts, including backup and restore workflows
  compute/     fire_fuse_hosts, including the runtime release checkpoints
  shared/      Tunnels and the Netdata dashboard, which span host groups
roles/
  builder/  storage/  compute/  shared/
```

`ansible.cfg` puts every role group on `roles_path`, so playbooks refer to
roles by name alone. Roles locate repository files through `role_path`, never
`playbook_dir`, so they behave the same from a host playbook or a single
playbook.

Deploy hosts in dependency order. Each command accepts `--check --diff`:

```sh
ansible-playbook --inventory inventory/staging/hosts.yml --ask-vault-pass storage.yml
ansible-playbook --inventory inventory/staging/hosts.yml --ask-vault-pass compute.yml
ansible-playbook --inventory inventory/staging/hosts.yml --ask-vault-pass builder.yml
```

From the repository root, `make deploy-storage`, `make deploy-compute`, and
`make deploy-builder` run the same commands. Set `ANSIBLE_INVENTORY` to
choose an inventory and `ANSIBLE_ARGS` for flags such as `--check --diff`.

The tunnel playbooks converge both peers, so a host playbook also touches the
other end of its tunnels. `compute.yml` installs the gateway, observer, and
edge Tunnel, which require an active runtime. On a new compute host, run
`compute.yml --tags base`, then the release checkpoints described below, then
`compute.yml` again.

Workspace mounts require the [pinned data-only JuiceFS build](../../storage/juicefs/README.md).
Build that artifact before deploying the gateway and runtime; an ordinary
JuiceFS mount now fails Workspace attachment. Managed Sessions keep UID/GID
`10001:10001`.

## Shared role and script helpers

Repeated checks and installs live in one place:

- `test_plugins/trusted_path.py` provides the `trusted_file` and
  `trusted_directory` Jinja tests. After a `follow: false` stat, assert
  `result.stat is trusted_file(owner='root', group='root', mode='0600')`
  instead of spelling out the exists, type, symlink, owner, group, and mode
  checks. `modes=[...]` accepts any listed mode.
- `test_plugins/fire_fuse_release_manifest.py` is the single definition of the
  staged release manifest shape that `fire_fuse_stage` writes and
  `fire_fuse_promote` trusts.
- The `storage_backup_common` role, a meta dependency of the
  `storage_backup_*` roles, holds the fixed staging, completed, evidence,
  restore, and program paths as role vars and refuses extra-vars overrides.
  Its `install_programs` task file installs scripts from `scripts/` into
  `/usr/local/lib/fire-fuse` together with `storage_script_common.py`.
- `scripts/storage_script_common.py` holds the validation helpers the storage
  scripts share. Any role that installs one of those scripts must install the
  module beside it; `install_programs` does this.
- `tests/support.py` loads hyphenated scripts and looks up parsed tasks by name
  for the role tests. Run them with
  `python3 -m unittest discover -s platform/ansible/tests`.

## Provider edge firewall

The `hetzner_robot_firewall` role owns a narrow provider-edge boundary for the
public RPC and NFS ports. It drops TCP/UDP 111 and 2049 for IPv4 and IPv6 while
leaving other ingress unchanged, so it can be applied without guessing the
operator's durable SSH source ranges. Robot firewall updates replace the whole
ruleset: the role reads first and refuses to overwrite rules it does not own.
The exact readback also requires Hetzner's provider-injected IPv4 and IPv6
outbound blocks for SMTP ports 25 and 465. The role does not submit duplicate
copies of those provider rules. It then polls Robot until the exact effective
policy is active. Put the numeric server id in inventory and these credentials
only in encrypted Ansible Vault variables:

```yaml
vault_hetzner_robot_user: replace-with-robot-webservice-user
vault_hetzner_robot_password: replace-with-robot-webservice-password
```

Audit and apply the provider boundary separately from host provisioning:

```sh
ansible-playbook --inventory inventory/staging/hosts.yml --ask-vault-pass \
  --check playbooks/compute/edge-firewall.yml
ansible-playbook --inventory inventory/staging/hosts.yml --ask-vault-pass \
  playbooks/compute/edge-firewall.yml
```

Check mode performs a read-only Robot API comparison. If the server already has
an independently managed policy, merge these denies into that policy deliberately
instead of allowing the role to replace it.

## Runtime configuration encryption key

The runtime encrypts customer secrets in SQLite with a stable 256-bit host key.
`playbooks/compute/prepare-service.yml` installs the Vault-supplied key at
`/etc/fire-fuse/config-encryption.key` as root-owned mode `0600` while the
service is inactive and refuses to replace a different existing key.
`stage-runtime.yml` only verifies that installed key and leaves it untouched.
Preserve this value with the state database;
losing it makes stored customer secrets unrecoverable.

## Activation gateway and edge Tunnel

The `fire_fuse_gateway` role installs the private App activation gateway. It
accepts only a checksum-pinned, controller-local gateway artifact, requires the
base `fire-fuse.service` to be enabled and running, and qualifies loopback
readiness before completing. Run `playbooks/compute/gateway.yml` after the runtime has
been activated. See
[`docs/gateway.md`](../../docs/gateway.md) for variables and the deployment
command. The role also requires a dedicated 64-character lowercase hexadecimal
`fire_fuse_gateway_edge_token` and installs it group-readable only by the
gateway.

The `cloudflare_edge_tunnel` role installs a checksum-pinned `cloudflared`
binary and the remotely managed Tunnel token, then runs an unprivileged,
outbound-only connector to the loopback gateway. It requires connected
readiness on `127.0.0.1:20241` before deployment succeeds. Run
`playbooks/compute/edge-tunnel.yml` only after the Cloudflare Terraform root and the
edge-aware gateway have been applied. See
[`platform/cloudflare/README.md`](../cloudflare/README.md) for the complete order.

## Control worker on the builder VM

The customer control API lives in the console stack. No customer API is
published directly from the VM host.

The `cantelop_control_worker` role installs the durable provider worker as a
standalone, checksum-pinned Linux binary on a separate Hetzner Cloud control/build
VM. It runs as the
dedicated `cantelop-control` system user, reads one root-owned mode `0640`
environment file, and reaches compute only through authenticated
`10.77.0.1:8084` over WireGuard. Its systemd preflight validates every provider configuration
and performs read-only PostgreSQL and private artifact-bucket connectivity
checks before the service can start. The R2 check uses `HeadBucket`; it neither
creates nor reads an artifact. The role then qualifies the authenticated
compute-ingress health route and requires the worker to remain active.

Provision one Ubuntu 24.04 x86-64 Hetzner Cloud VM with a dedicated, mounted
250 GB volume at `/var/lib/cantelop-builder`. Run
`playbooks/builder/prepare-control-builder.yml` during host preparation and
whenever the materializer changes; it refuses to run without that mount. The
`control_builder` role puts Docker data,
conversion work, and prepared images on the volume and installs the root-owned
converter as a separate service. The worker reaches it through a group-restricted
Unix socket; the worker retains `NoNewPrivileges=yes` and has no sudo access.
The VM needs 2–4 vCPUs and 4–8 GiB RAM to start, with one conversion at a time.

Configure the two peers in `inventory/staging/hosts.yml` as shown in the example.
Store private keys in Vault, install the tunnel on both hosts, and verify it
before starting the new Fire Fuse release or the relocated worker:

```sh
ansible-playbook --inventory inventory/staging/hosts.yml --ask-vault-pass playbooks/shared/wireguard.yml
```

Fire Fuse binds provider ingress only to `10.77.0.1:8084`; its customer gateway
remains on loopback. The builder initiates the tunnel to the dedicated host's
public UDP 51820 endpoint. The provider API itself has no public listener.

Build the pinned worker artifact on the controller:

```sh
./scripts/build-builder-linux-amd64.sh
```

Add the generated artifact and checksum plus the non-secret provider settings
shown in `inventory/staging/hosts.example.yml`. Store these values only in
encrypted Ansible Vault variables:

```yaml
vault_cantelop_database_url: postgresql://...
vault_cantelop_config_encryption_key: replace-with-base64url-32-byte-key
vault_cantelop_artifact_access_key_id: replace-with-r2-access-key
vault_cantelop_artifact_secret_access_key: replace-with-r2-secret-key
vault_cantelop_cloudflare_api_token: replace-with-workers-token
vault_fire_fuse_operator_token: replace-with-64-lowercase-hex
vault_fire_fuse_gateway_runtime_token: replace-with-distinct-64-lowercase-hex
vault_fire_fuse_dispatcher_runtime_token: replace-with-distinct-64-lowercase-hex
vault_fire_fuse_observer_runtime_token: replace-with-distinct-64-lowercase-hex
vault_fire_fuse_compute_ingress_token: replace-with-distinct-64-lowercase-hex
vault_fire_fuse_gateway_product_token: replace-with-distinct-64-lowercase-hex
vault_fire_fuse_observability_ingest_token: replace-with-distinct-64-lowercase-hex
vault_fire_fuse_wireguard_private_key: replace-with-base64-wireguard-key
vault_control_builder_wireguard_private_key: replace-with-distinct-base64-wireguard-key
```

When replacing the legacy shared control token, do not run the component
playbooks independently. The coordinated cutover first validates the complete
effective secret set, fixed service identities, every controller artifact and
migration-file checksum, every worker setting, the installed runtime checksum
and configuration, and observer TLS files without changing the host. Only after both
the compute-host and `control_builder_hosts` preflights pass does it stop the
builder's control worker and every old compute-host credential reader, install
the scoped server and client copies, start the runtime and dependent services in
order, and finally restart the builder's worker with the dedicated
compute-ingress credential over WireGuard:

```sh
ansible-playbook \
  --inventory inventory/staging/hosts.yml \
  --ask-vault-pass \
  playbooks/shared/control-credential-cutover.yml
```

Configure Vercel Production with the same
`vault_fire_fuse_gateway_product_token` value as
`CANTELOP_GATEWAY_PRODUCT_CONTROL_TOKEN`. No host runtime token belongs in
Vercel. Rolling back to units with `SupplementaryGroups=fire-fuse-control`
reopens the security boundary and is not a fail-closed rollback.

`builder.yml` still converges the full host. For a worker-only release,
`playbooks/builder/control-worker.yml` reconciles only the worker and its
migrations, without reprovisioning Docker or the build materializer. A cohort
that changes the materializer must explicitly run
`prepare-control-builder.yml` in the approved dependency order. The
control-worker role installs a separately checksum-pinned migration runner
and a checksum-addressed copy of the checked-in Drizzle migration directory.
The runner delegates migration ordering, hashing, transactions, and bookkeeping
to Drizzle while retaining the bounded PostgreSQL advisory lock used to
serialize deployments. Pending migrations run before the worker restarts. An
already installed checksum-addressed migration bundle is verified in place;
Ansible copies it only when the bundle is absent or its files fail validation.
Apply the role only after the matching Fire Fuse service is healthy:

```sh
ansible-playbook \
  --inventory inventory/staging/hosts.yml \
  --ask-vault-pass \
  --check \
  --diff \
  playbooks/builder/control-worker.yml

ansible-playbook \
  --inventory inventory/staging/hosts.yml \
  --ask-vault-pass \
  playbooks/builder/control-worker.yml
```

The control worker has no public listener. Vercel serves the customer control
API; the VM worker claims durable PostgreSQL operations and calls Cloudflare,
private artifact storage, and Fire Fuse over WireGuard.

## Host baseline and storage services

The Ansible controller requires `jq`; the staging role uses it to validate the
checksum-pinned container process contract in both normal and check mode before
rendering the service configuration.

The `host_base` role establishes the common Ubuntu host contract:

- Ubuntu 24.04 on x86-64 with `/dev/kvm`;
- baseline host packages;
- the dedicated non-root jailer account;
- root-owned configuration, image, runtime, and state directories; and
- persistent IPv4 forwarding.

`/var/lib/fire-fuse` uses mode `0751` on compute. On the storage host,
Redis and MinIO data directories live on the dedicated mounted filesystem
and remain service-owned mode `0750`, so neither service can read the other's
data.

Redis and MinIO now run on the dedicated `storage_hosts` inventory group.
The compute host connects through `wg-storage` (`10.78.0.1` to `10.78.0.2`).
Both services bind the storage peer address; the compute host runs only the
JuiceFS client, its local cache, NFS exports, and sandbox runtime. The completed
one-way migration is recorded in
[`docs/history/storage-host-cutover.md`](../../docs/history/storage-host-cutover.md).

The `redis_metadata` role installs the Ubuntu Redis package but replaces its
distribution service with the hardened fire-fuse unit. It owns the Redis
configuration, persistent data directory, root-only password file, and
root-only JuiceFS metadata URL. The service binds only to loopback and the
private storage address, uses authenticated connections, enables RDB and AOF
persistence, and refuses eviction when its configured memory ceiling is
reached.

The `minio_object` role installs a checksum-verified MinIO server artifact and
a checksum-pinned official client binary, creates a dedicated service identity
and persistent data directory, and runs a hardened single-node MinIO service.
Its S3 API listens only on `10.78.0.2:9000`; the administrative console
listens only on `127.0.0.1:9001`. The role verifies readiness and idempotently
creates the private `fire-fuse-workspaces` bucket.

JuiceFS receives a separate `juicefs` access key and secret. The role attaches
MinIO's built-in `readwrite` policy to that user and verifies bucket access with
the non-administrative credential. The MinIO root credential is retained only
for service administration and reconciliation. Once the `juicefs` user exists,
the provisioning role rejects a different or unusable secret instead of
silently rotating it: MinIO and the formatted JuiceFS metadata must never
disagree about that credential.

The `juicefs_volume` role installs the same checksum-pinned JuiceFS client used
in the guest image. The selected release lives in
[`roles/storage/juicefs_volume/defaults/main.yml`](roles/storage/juicefs_volume/defaults/main.yml)
and the controller artifact is produced by
[`prepare-juicefs-linux-amd64.sh`](../../storage/juicefs/prepare-juicefs-linux-amd64.sh).
The role formats the shared volume against Redis database 1 and the private
MinIO bucket. It reads Redis before formatting and only runs
`juicefs format --no-update` when the database is empty. Every run then parses
`juicefs status` and requires the configured volume name, S3 storage type, and
exact MinIO bucket URL. A nonempty database that is not the expected JuiceFS
volume fails closed.

The `juicefs_gateway` role mounts that formatted volume once on the host at
`/var/lib/fire-fuse/workspaces`, backed by a host-local JuiceFS cache. Its
root-only environment file supplies the Redis credential without placing it in
the service command line. It also installs a TCP-only NFSv4.1/4.2 server whose
service depends on the host mount. Before the NFS package can bind, a persistent
iptables boundary drops TCP and UDP port 111 for IPv4 and IPv6, then inserts a
port-2049 jump at the head of IPv4 `INPUT`; unmatched NFS clients are dropped.
After installing the NFS package, the role stops and masks `rpcbind.service`
and `rpcbind.socket` because the server supports only NFSv4. The deployment
fails unless both units remain masked and inactive and port 111 has no TCP or
UDP listeners. The server listens on the IPv4 wildcard so dynamic TAP
addresses work, while per-VM rules match the exact TAP and guest address inside
the dedicated chain ahead of its terminal deny. Its managed export table
remains deliberately empty. Future per-workspace exports are constrained to
`rw,async,no_subtree_check,no_root_squash`; exporting the shared parent is
forbidden. fire-fuse dynamically installs and removes exact workspace exports
and matching guest-IP firewall allowances. Run `playbooks/compute/workspace-gateway.yml` only after
`playbooks/storage/storage.yml` has provisioned and formatted the backends.

## Observability and the Netdata dashboard

The `storage_observability` role installs minute-level Redis and MinIO probes
that emit structured JSON to the `fire-fuse-storage-observer` journal
identifier. The probes report authenticated Redis availability and persistence
state, MinIO readiness, probe latency, and bounded capacity signals without
installing a monitoring backend. Each probe also atomically publishes its
latest sanitized aggregate under `/run/fire-fuse-observability`; these volatile
snapshots contain no credentials or per-VM identifiers. The storage playbook
runs both probes once so an unhealthy backend fails deployment.

After runtime activation, the same role enables a host JuiceFS gateway
observer. It scrapes the gateway's loopback-only metrics endpoint and emits
cache usage, cache hit/miss counters, and object-store error counters for the
single shared host client. Neither observer passes credentials in arguments or
adds backend-management behavior to fire-fuse.

The optional `netdata_dashboard` role installs the checksum-pinned Netdata
release selected in
[`roles/shared/netdata_dashboard/defaults/main.yml`](roles/shared/netdata_dashboard/defaults/main.yml)
as a small local dashboard over those aggregate snapshots. It enables only
basic host collectors and the custom fire-fuse collector, disables machine
learning, Cloud connectivity, telemetry, automatic updates, and stock alerts,
and limits Netdata to 25% of one CPU, 256 MiB of memory, low I/O weight, and a
single 256 MiB metrics tier. The checksum-pinned static installation occupies
approximately 600 MiB before metrics; reserve at least 1.25 GiB total. The
dashboard listens only on
`127.0.0.1:19999`; use an SSH tunnel rather than exposing it to the guest or
public networks. Netdata is provisioned independently and fire-fuse neither
starts nor manages it.

The dashboard covers all three machines. Compute runs the Netdata parent;
the storage and builder agents are configured concurrently after compute and
stream to it over `wg-storage` and `wg0`
respectively. Each agent reports only the services it runs: fire-fuse and the
JuiceFS gateway on compute, Redis and MinIO on storage (from the storage
host's own probe snapshots), and the control worker and build materializer on
the builder. The parent additionally listens on `10.78.0.1` and `10.77.0.1`,
accepting only streaming connections and only from `10.78.0.2` and
`10.77.0.2`; the dashboard and API stay on loopback on every machine. Store a
lowercase UUID (`uuidgen | tr A-F a-f`) as `vault_netdata_stream_api_key`,
readable by all three host groups. Run `storage-wireguard.yml` and
`wireguard.yml` first; the playbook ends by requiring both children on the
parent.

Install or reconcile the dashboard after storage and runtime activation:

```sh
ansible-playbook \
  --inventory inventory/staging/hosts.yml \
  --check \
  --diff \
  playbooks/shared/dashboard.yml

ansible-playbook \
  --inventory inventory/staging/hosts.yml \
  playbooks/shared/dashboard.yml
```

For per-task wall times on a rollout, run each playbook with
`ANSIBLE_CALLBACKS_ENABLED=task_timings` set. The repository callback prints
every task at the end, longest first; save the output with the deployment log.

On a host without Netdata, check mode validates inputs and reports that the
installer must run before its configuration can be previewed. It does not
download or install Netdata. After an initial install, check mode previews
configuration changes. To verify existing streams without reconciling all
three roles, run:

```sh
ansible-playbook \
  --inventory inventory/staging/hosts.yml \
  playbooks/shared/dashboard-verify-streams.yml
```

The stream check reports each child's sample age or HTTP error and gives both
children 12 attempts, five seconds apart. Use the full dashboard playbook
for configuration changes and release upgrades.

Then open a local tunnel:

```sh
ssh -N -L 19999:127.0.0.1:19999 root@fire-fuse-host
```

Browse to `http://127.0.0.1:19999`. Run the dashboard playbook a second time
and require zero changed tasks. A release upgrade requires changing both the
pinned release and SHA-256 in the role defaults; the Netdata updater remains
masked.

## Runtime release overview

The `fire_fuse_stage` role verifies all runtime artifacts on the controller,
copies them into `/var/lib/fire-fuse/staged-release`, renders the proposed
strict host configuration, and writes a release manifest containing every
eventual destination, mode, and SHA-256. It then verifies the staged copies
again on the host. Staging does not replace live binaries, boot images, host
configuration, credentials, documentation, or systemd units, and it does not
restart or enable fire-fuse. Provision `host_base` and run
`playbooks/compute/prepare-service.yml` separately before staging a new host.

Before staging, a read-only helper compares the proposed guest address pool
with every nondefault IPv4 route on the host. Overlap with host, VPN, container,
policy-routing, blackhole, or datacenter routes fails the play. Routes on an
active TAP are ignored only when its deterministic name and
`fire-fuse:<vm-id>` alias prove fire-fuse ownership; this permits restaging the
same pool while existing managed VMs are still running.

## Storage durability and backups

The dedicated storage machine remains a single-node, single-drive layout.
It provides persistence across compute-host lifecycles, but it does not provide
storage redundancy or survive loss of the storage disk. The S3 API is plaintext inside the private WireGuard tunnel and the built-in `readwrite` policy is not scoped to
one bucket. Both choices assume the current trusted, non-multi-tenant staging
environment and must be revisited before adding tenant boundaries.

The backup and retention contract is defined in
[`docs/storage-backup.md`](../../docs/storage-backup.md). The first supported
off-host boundary is a separately provisioned, Object-Lock-enabled
S3-compatible bucket. No backup timer or remote bucket is installed by these
playbooks yet. Redis AOF/RDB and the local MinIO directory do not satisfy the
contract, and fire-fuse never receives backup credentials.

## Install the off-host backup destination boundary

Provision an S3-compatible bucket in a different failure domain, enable
versioning and Object Lock there, and create a credential dedicated to that
bucket. The playbook does not perform those remote administrative actions.

Add the non-secret connection values to host or group inventory:

```yaml
storage_backup_destination_endpoint_url: https://s3-backup.example.com
storage_backup_destination_region: example-1
storage_backup_destination_bucket: fire-fuse-storage-backups
storage_backup_destination_prefix: shared-volume/backup-sets
```

Add the dedicated credential values to the existing ignored Ansible Vault
file:

```yaml
storage_backup_destination_access_key_id: replace-with-backup-access-key
storage_backup_destination_secret_access_key: replace-with-backup-secret-key
```

Preview and apply only the local destination boundary:

```sh
ansible-playbook \
  --inventory inventory/staging/hosts.yml \
  --ask-vault-pass \
  --check \
  --diff \
  playbooks/storage/backup-destination.yml

ansible-playbook \
  --inventory inventory/staging/hosts.yml \
  --ask-vault-pass \
  playbooks/storage/backup-destination.yml
```

The role creates the root-only operations directory, renders the public
descriptor separately from the mode-`0600` credential, installs the strict
validation and qualification tools, and validates the local files after
installation. It does not install a timer, upload a backup, create or modify
the bucket, or expose either file to fire-fuse. Run the playbook a second time
and require zero changed tasks.

Then run the remote read-only qualification:

```sh
ansible-playbook \
  --inventory inventory/staging/hosts.yml \
  --ask-vault-pass \
  playbooks/storage/qualify-backup-destination.yml
```

This playbook rejects check mode, verifies the ownership and modes of every
local input, requires the pinned MinIO client SHA-256, reruns local validation,
and requires the remote bucket versioning, COMPLIANCE retention, and
credential-scope probes to pass. It emits only the non-secret qualification
evidence. It does not upload or mutate an object.

After reviewing that evidence, explicitly run the controlled-write workflow:

```sh
ansible-playbook \
  --inventory inventory/staging/hosts.yml \
  --ask-vault-pass \
  playbooks/storage/qualify-backup-destination-write.yml
```

This separate playbook rejects check mode and creates one retained remote
marker, so it always reports a changed task. It requires at least 14 days of
COMPLIANCE retention, proves the exact version cannot be deleted, and prints
the object key, version ID, SHA-256, and retention deadline. Preserve that
output as evidence. Remove only that exact version after both the deadline and
evidence review; never clean qualification markers with a wildcard.

## Enter the backup capture consistency boundary

The internal `storage_backup_capture_preflight` role implements the maintenance
entry checks required before local capture. It requires an explicit
`storage_backup_capture_maintenance_authorized: true`, a normal Ansible run,
healthy Redis, MinIO, and fire-fuse services, a ready loopback API, no active
microVM lifecycle, no host JuiceFS mounts, and the checksum-pinned JuiceFS
client.

After stopping fire-fuse, it checks the services again and requires
`juicefs status` to report the expected volume and an empty `Sessions` array.
It then publishes `storage_backup_capture_maintenance`, including the volume
UUID and maintenance timestamp, for the capture role in the same play. If any
post-stop check fails, it restarts fire-fuse and requires readiness before
reporting failure.

This role intentionally has no standalone playbook yet. A successful run
leaves fire-fuse stopped, so it will be exposed only inside the complete
capture workflow with an unconditional service-restoration block.

The internal `storage_backup_capture_metadata` role is the first capture step
inside that boundary. It requires the maintenance fact, rechecks service,
mount, session, volume-name, and volume-UUID state, creates a unique root-only
staging directory, and runs the pinned `juicefs dump --binary` into a partial
file. It rejects an existing set directory instead of overwriting or guessing
whether to resume.

Before atomically promoting `metadata.dump`, the role restricts it to mode
`0600` and rejects either the Redis password or MinIO JuiceFS secret appearing
as plaintext. It then publishes the artifact's relative and absolute paths,
size, SHA-256, and JuiceFS client version for later manifest construction. This
role also has no standalone playbook; the staged dump is not yet a complete or
off-host backup.

The internal `storage_backup_capture_redis` role follows the metadata dump. It
requires the same maintenance and metadata-artifact facts, issues synchronous
Redis `SAVE`, and waits for zero unsaved changes with no RDB save or AOF rewrite
in progress. Both RDB and multi-part AOF files must be present.

The role checksums every Redis persistence file before and after creating a
mode-`0600` archive and rejects the capture if any path, size, or checksum
changes. It also requires healthy RDB/AOF status after the copy, verifies the
archive contains both persistence formats, and publishes the archive path,
size, SHA-256, and Redis version. The archive remains a sensitive transient
artifact and is not a complete or off-host backup.

The internal `storage_backup_capture_minio` role follows both local capture
steps. It revalidates their checksums, the installed pinned MinIO binary, and
the trusted MinIO data tree. It then stops MinIO while fire-fuse remains
stopped and creates a mode-`0600` raw archive of the complete data directory,
including the workspace bucket and `.minio.sys` metadata.

The role compares the archive against the stopped source before promoting it
atomically. MinIO is restarted and must pass its readiness endpoint after
every snapshot attempt, including a failed one; fire-fuse intentionally
remains stopped for the eventual complete-capture wrapper. A raw archive is
used instead of a logical mirror so object versions and MinIO's internal
metadata are preserved without adding another backup-time client dependency.

The internal `storage_backup_capture_restore_configuration` role follows all
three storage captures. It requires a dedicated mode-`0600` Ansible Vault
password file on the controller, rechecks every preceding artifact, and
encrypts the recovery-critical storage credentials, volume identity, component
versions, and binary checksums without creating a plaintext file.

The role decrypts the generated ciphertext in controller memory and requires
an exact JSON round trip before installing `restore-secrets.vault` as a
root-only artifact. The Vault password is never copied to the storage host and
must be retained separately from both the host and off-host backup bucket.
This internal role also remains unavailable through a standalone playbook.

The internal `storage_backup_capture_manifest` role is the final local capture
step. It revalidates all four artifact facts and files, installs the repository's
strict verifier with an exact controller-to-host checksum, and constructs the
version-1 manifest from an explicit field allowlist. It scans the candidate
manifest for every active storage credential before invoking the verifier.

Only a candidate whose schema, paths, sizes, checksums, source identity, and
component set all verify is atomically promoted to `manifest.json`. The
published `storage_backup_capture_complete_set` fact identifies a complete
local set, not an off-host or immutable backup. This role also has no
standalone playbook while fire-fuse restoration is not yet guaranteed.

The `storage_backup_capture` wrapper and
`playbooks/storage/capture-storage-backup.yml` now provide that recovery boundary. The
wrapper acquires an exclusive root-only `/run/lock` directory, generates a
timestamp-plus-random set ID, invokes the six internal roles in order, and
always restarts fire-fuse and requires `/readyz`. A successful set is verified
again before the lock is released.

The playbook refuses check mode and requires explicit maintenance
authorization. It also requires a dedicated controller-side Vault password
file with mode `0600`:

```sh
cd platform/ansible
ansible-playbook \
  -i inventory/staging/hosts.yml \
  playbooks/storage/capture-storage-backup.yml \
  -e '{"storage_backup_capture_maintenance_authorized":true,"storage_backup_capture_restore_configuration_vault_password_file":"/absolute/controller/path/restore-vault-password"}' \
  --ask-vault-pass
```

Drain every VM before running it; the preflight verifies the drain and never
terminates guests itself. On capture failure, fire-fuse is restarted and
readiness-checked before the play reports failure, and the incomplete staging
set is retained for inspection. If service recovery or final verification
fails, the lock deliberately remains; inspect the host before removing that
exact empty lock directory.

Transfer is deliberately separate from capture so off-host bandwidth does not
extend the VM maintenance window. After a local capture succeeds, transfer its
reported set ID:

```sh
cd platform/ansible
ansible-playbook \
  -i inventory/staging/hosts.yml \
  playbooks/storage/transfer-storage-backup.yml \
  -e storage_backup_transfer_set_id=backup-20260728T120000Z-0123abcd \
  --ask-vault-pass
```

The `storage_backup_transfer` role revalidates the local root-only set and the
remote destination on every run. It acquires a per-set host lock, uploads and
streams back all four artifacts, verifies each SHA-256 and COMPLIANCE retention
period, then uploads and verifies `manifest.json` last. A remote manifest is
the off-host completeness marker.

If a partial attempt exists without a remote manifest, retrying is safe: the
versioned immutable destination may retain older artifact versions, while the
new current versions are verified before the manifest is published. If the
remote manifest already exists, the workflow performs a read-only reconciliation
of that version and all four artifacts; it never creates another version.

After remote verification, an internal bookkeeping role atomically creates
`/var/lib/fire-fuse/storage-backups/completed/SET-ID.json`. This root-only
record contains the local manifest checksum plus remote object version,
checksum, and retention evidence. The backup bytes remain unchanged beneath
the staging root. Re-running the workflow accepts an existing record only when
it exactly matches current local and remote verification evidence. This
workflow performs no remote or local deletion.

Audit the resulting local inventory before considering any retention action:

```sh
cd platform/ansible
ansible-playbook \
  -i inventory/staging/hosts.yml \
  playbooks/storage/audit-storage-backups.yml \
  --ask-vault-pass
```

The read-only auditor classifies each set as `incomplete`, `complete_local`,
`transferred`, or `inconsistent`. It verifies root ownership and modes,
complete-set manifests, completion-record shape, and the exact binding between
each local manifest and its recorded remote versions. Unexpected entries and
orphan records are inconsistent. The play reports the full redacted inventory
and fails if any inconsistency exists; incomplete captures are reported but
are not modified.

After a restore rehearsal has succeeded, produce a dry-run retention plan by
supplying the transferred set used for that rehearsal:

```sh
cd platform/ansible
ansible-playbook \
  -i inventory/staging/hosts.yml \
  playbooks/storage/plan-storage-backup-retention.yml \
  -e storage_backup_plan_retention_restore_qualified_set_id=backup-20260728T120000Z-0123abcd \
  --ask-vault-pass
```

The supplied set ID selects the canonical root-only evidence file at
`/var/lib/fire-fuse/storage-backups/restores/SET-ID.json`; it is no longer
sufficient as an assertion by itself. The planner validates that record
against the exact local manifest, transfer record, source volume UUID, remote
prefix, isolated target, mount/remount marker hashes, and unchanged-production
guard. Until the restore workflow generates this evidence, planning fails
closed.

For the normal end-to-end qualification, run all restore gates with one
transferred backup set ID:

```sh
cd platform/ansible
ansible-playbook \
  -i inventory/staging/hosts.yml \
  playbooks/storage/qualify-storage-backup-restore.yml \
  -e storage_backup_qualify_restore_set_id=backup-20260728T120000Z-0123abcd \
  --ask-vault-pass
```

The playbook contains no new restore logic; it sequences the independently
reviewable roles below and propagates the generated rehearsal ID as an Ansible
fact. It stops on the first failed gate and deliberately leaves that isolated
rehearsal available for inspection or manual continuation. Cleanup is never
automatic and remains a separate post-evidence command.

Use the individual stage commands below when reviewing the workflow or
continuing a failed rehearsal from its first incomplete stage.

Start only the materialization stage by fetching the exact recorded off-host
versions into a new isolated directory:

```sh
cd platform/ansible
ansible-playbook \
  -i inventory/staging/hosts.yml \
  playbooks/storage/materialize-storage-backup-restore.yml \
  -e storage_backup_materialize_restore_set_id=backup-20260728T120000Z-0123abcd \
  --ask-vault-pass
```

The role revalidates the local set and completion record, requalifies the
configured backup destination, and fetches the recorded version of the
manifest and each artifact. It verifies sizes and SHA-256 checksums before
atomically publishing a redacted `materialization.json` inside the new
root-only rehearsal directory. Before downloading, it hashes a normalized
production JuiceFS configuration identity from the trusted metadata URL and
`juicefs status`. That identity must match the downloaded manifest before
`production-guard-before.json` is published. The guard contains no credential
or raw metadata URL and deliberately excludes changing session and usage
counters. It does not decrypt secrets, extract archives, or start Redis,
MinIO, JuiceFS, or fire-fuse.

Extract the raw MinIO snapshot only after materialization reports its generated
rehearsal ID:

```sh
cd platform/ansible
ansible-playbook \
  -i inventory/staging/hosts.yml \
  playbooks/storage/extract-minio-storage-restore.yml \
  -e storage_backup_extract_minio_restore_rehearsal_id=restore-20260728T130000Z-89abcdef \
  --ask-vault-pass
```

The extraction role validates the materialization and manifest bindings,
requires the object snapshot identity to equal the MinIO artifact checksum,
and rejects unsafe tar members before publishing `minio-extraction.json`.
Files are written only beneath the new rehearsal's
`services/minio/data` directory. This step does not start a service or access
the production MinIO data directory.

Start and qualify the isolated restore instance with the same rehearsal ID:

```sh
cd platform/ansible
ansible-playbook \
  -i inventory/staging/hosts.yml \
  playbooks/storage/start-minio-storage-restore.yml \
  -e storage_backup_start_minio_restore_rehearsal_id=restore-20260728T130000Z-89abcdef \
  --ask-vault-pass
```

The role rehashes the extracted tree, requires the installed checksum-pinned
MinIO binary to report the release captured in the manifest, and starts a
rehearsal-specific systemd unit on loopback ports `19000` and `19001`.
Ephemeral root credentials remain in a mode-`0600` rehearsal file and are
redacted from Ansible output. The unit runs as `minio`, bind-mounts only the
extracted data into its filesystem sandbox, is not enabled at boot, and
cannot use non-loopback IP addresses. Readiness and the captured bucket must
both qualify before `minio-runtime.json` is published. A qualification failure
stops the isolated unit and retains its files for inspection.

Copy the captured bucket into a new empty recovery bucket:

```sh
cd platform/ansible
ansible-playbook \
  -i inventory/staging/hosts.yml \
  playbooks/storage/copy-minio-storage-restore.yml \
  -e storage_backup_copy_minio_restore_rehearsal_id=restore-20260728T130000Z-89abcdef \
  --ask-vault-pass
```

The role reads only the rehearsal's ephemeral credentials and loopback
endpoint. It refuses an existing recovery bucket, creates the deterministic
`fire-fuse-recovery-<rehearsal>` bucket, and mirrors the current captured
objects without overwrite or removal flags. It then streams and hashes every
object in the source before and after the copy and every object in the target.
Only identical object names, sizes, contents, counts, and aggregate bytes
produce `minio-recovery-bucket.json`. A partial failure remains available for
inspection and must not be silently resumed.

Restore and engine-qualify the captured Redis persistence:

```sh
cd platform/ansible
ansible-playbook \
  -i inventory/staging/hosts.yml \
  playbooks/storage/restore-redis-storage-snapshot.yml \
  -e storage_backup_restore_redis_snapshot_rehearsal_id=restore-20260728T130000Z-89abcdef \
  --ask-vault-pass
```

The role safely extracts `redis-persistence.tar.gz`, requires `dump.rdb` and
multi-part AOF files, rehashes the extracted tree, and requires the installed
Redis server to match the captured release. It starts a rehearsal-specific,
unprivileged systemd unit on loopback port `16379` with an ephemeral password.
Redis must load the persistence, report healthy AOF state, and expose restored
keys in database 1 before `redis-runtime.json` is published. The unit is not
enabled at boot, and any qualification or production-state guard failure
stops it while retaining the rehearsal files.

Load the portable JuiceFS metadata dump into a second empty Redis instance:

```sh
cd platform/ansible
ansible-playbook \
  -i inventory/staging/hosts.yml \
  playbooks/storage/restore-juicefs-metadata.yml \
  -e storage_backup_restore_metadata_rehearsal_id=restore-20260728T130000Z-89abcdef \
  --ask-vault-pass
```

The role checksum-verifies `metadata.dump` and the pinned JuiceFS client,
starts a distinct empty Redis service on loopback port `16380`, proves
database 1 is empty, and imports the binary dump. It then supplies only the
ephemeral rehearsal MinIO credentials to `juicefs config`, redirecting the
restored volume to the qualified recovery bucket. `juicefs status` must report
the captured name and UUID, S3 storage, the recovery bucket, and zero sessions
before `metadata-runtime.json` is published. Production Redis, production
MinIO, and the snapshot-validation Redis service are state-guarded.

Exercise the isolated restored volume through a cold-cache remount:

```sh
cd platform/ansible
ansible-playbook \
  -i inventory/staging/hosts.yml \
  playbooks/storage/qualify-juicefs-restore-mount.yml \
  -e storage_backup_qualify_restore_mount_rehearsal_id=restore-20260728T130000Z-89abcdef \
  --ask-vault-pass
```

The role binds the metadata runtime to its fixed loopback Redis endpoint and
rehearsal-specific service, then mounts only beneath
`/mnt/fire-fuse-restores/`. It hashes an existing nonempty file, writes and
flushes a marker, cleanly unmounts, and remounts with a different empty cache.
Both hashes must survive. Marker removal bypasses the JuiceFS trash, and the
role requires a final empty, unmounted mountpoint plus unchanged production
and restore service states before publishing `mount-runtime.json`.

Finalize the rehearsal only after every preceding record exists:

```sh
cd platform/ansible
ansible-playbook \
  -i inventory/staging/hosts.yml \
  playbooks/storage/finalize-storage-backup-restore.yml \
  -e storage_backup_finalize_restore_rehearsal_id=restore-20260728T130000Z-89abcdef \
  --ask-vault-pass
```

Finalization requires root-owned, mode-`0600` inputs and binds every stage to
the same rehearsal and backup set. It recaptures the normalized production
JuiceFS configuration guard, requires an exact payload and digest match,
checks that production and isolated storage services remain running, and
publishes `/var/lib/fire-fuse/storage-backups/restores/SET-ID.json`. The
existing fail-closed validator immediately revalidates that file against the
original manifest and transfer record. Rejected evidence is removed and
cannot unlock retention planning.

After reviewing the canonical evidence, remove only that isolated rehearsal:

```sh
cd platform/ansible
ansible-playbook \
  -i inventory/staging/hosts.yml \
  playbooks/storage/cleanup-storage-backup-restore.yml \
  -e storage_backup_cleanup_restore_set_id=backup-20260728T120000Z-0123abcd \
  -e storage_backup_cleanup_restore_rehearsal_id=restore-20260728T130000Z-89abcdef \
  --ask-vault-pass
```

Cleanup is destructive only within the named rehearsal. It first revalidates
the canonical evidence and requires its rehearsal ID to match, refuses a
mounted rehearsal path, stops and removes the three isolated systemd units,
and removes the rehearsal workspace, mountpoint, and caches. Production
service states must remain unchanged. Backup sets and canonical evidence are
preserved, and a root-only audit record is written beneath
`/var/lib/fire-fuse/storage-backups/restore-cleanups/`. A partial cleanup can
be retried until that completion record exists.

After that gate, the planner applies the conservative 14-daily, 8-weekly, and
12-monthly policy, always preserves the newest and restore-qualified sets, and
excludes any object whose recorded COMPLIANCE retention is still active.

The output explains every keep decision and labels eligible whole sets
`candidate_expire_complete_set`. It writes no plan file and invokes no local or
remote deletion command. Candidate output therefore authorizes review only,
not deletion.

## Inventory

Copy the example and replace its documentation-only address:

```sh
cd platform/ansible
cp inventory/staging/hosts.example.yml inventory/staging/hosts.yml
$EDITOR inventory/staging/hosts.yml
```

The real `hosts.yml` path is ignored. Keep it out of Git because it contains
host-specific release identities and changes independently from source.
SSH host-key checking is enabled; add the dedicated host's verified key to the
operator's `known_hosts` before running a playbook.

## Apply the host baseline

From `platform/ansible`, first inspect the proposed changes:

```sh
ansible-playbook \
  --inventory inventory/staging/hosts.yml \
  --check \
  --diff \
  playbooks/compute/host-base.yml
```

Then apply them:

```sh
ansible-playbook \
  --inventory inventory/staging/hosts.yml \
  playbooks/compute/host-base.yml
```

Run the same command again and require no changed tasks. That second run is the
initial idempotency qualification.

## Configure the private storage network

Install, start, and verify the WireGuard tunnel between compute and storage
hosts:

```sh
ansible-playbook \
  --inventory inventory/staging/hosts.yml \
  --check \
  --diff \
  playbooks/shared/storage-wireguard.yml

ansible-playbook \
  --inventory inventory/staging/hosts.yml \
  playbooks/shared/storage-wireguard.yml
```

## Configure Redis metadata storage

Redis requires an explicit secret stored in Ansible Vault. Create the ignored
group-variable file:

```sh
mkdir -p inventory/staging/group_vars/fire_fuse_hosts
ansible-vault create \
  inventory/staging/group_vars/fire_fuse_hosts/vault.yml
```

Its decrypted YAML must contain four distinct 64-character lowercase
hexadecimal secrets:

```yaml
redis_metadata_password: "replace-with-64-lowercase-hexadecimal-characters"
minio_object_root_password: "replace-with-another-64-lowercase-hex-value"
minio_object_juicefs_secret_key: "replace-with-a-third-64-lowercase-hex-value"
vault_fire_fuse_config_encryption_key: "replace-with-a-fourth-64-lowercase-hex-value"
```

The fire-fuse configuration encryption key is required by runtime staging.
Keep it stable across releases and include it in the protected recovery
material alongside the SQLite state database.

Preserve `minio_object_juicefs_secret_key` after the volume is formatted.
Changing only this Vault value is intentionally rejected because it would
replace the MinIO user's secret while JuiceFS metadata retained the old value.
Credential rotation requires a drained maintenance workflow that updates both
systems and then runs the host JuiceFS smoke test; that workflow is not part of
ordinary storage reconciliation. The low-level role accepts
`minio_object_allow_credential_rotation: true` only while the fire-fuse service
is stopped (or not yet installed). Do not set that variable in ordinary
inventory: it permits the MinIO credential mutation but does not itself drain
VMs, update JuiceFS metadata, provide rollback, run the smoke test, or restore
the service.

Preserve the exact Redis password stored in Ansible Vault after provisioning.
Drain all workspace-enabled VMs before intentionally changing it; running
guests retain the metadata URL delivered at boot and will not receive the
replacement credential. Ordinary Redis reconciliation now rejects a Vault
value that differs from the installed root-only password instead of implicitly
rotating it. Rotation belongs in a separate maintenance workflow that stops
fire-fuse, updates Redis and the metadata URL together, and qualifies the
volume before accepting new VMs. The low-level role accepts
`redis_metadata_allow_password_rotation: true` only while the fire-fuse service
is stopped (or not yet installed). Do not set that variable in ordinary
inventory: it permits the credential mutation but does not itself drain VMs,
provide rollback, run the JuiceFS smoke test, or restore the service.

Preview and apply the complete Redis provisioning path:

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

The role suppresses diffs and output for every task that handles the password.
After applying it, run the playbook again and require no changed tasks.

### Rotate the Redis credential

First drain every active microVM, then replace `redis_metadata_password` with a
new 64-character lowercase hexadecimal value in Ansible Vault. Run only the
dedicated maintenance playbook:

```sh
ansible-playbook \
  --inventory inventory/staging/hosts.yml \
  --ask-vault-pass \
  playbooks/storage/rotate-redis-credential.yml
```

The role requires fire-fuse and Redis to begin running and enabled, requires
`/readyz`, and independently refuses to continue while any VM is starting,
running, or stopping or while a host JuiceFS mount remains active. It saves the
trusted installed password, metadata URL, and Redis configuration in Ansible's
in-memory task state, then stops fire-fuse. The low-level Redis role rotates the
password and coupled metadata URL while the consumer is offline. A normal host
JuiceFS client must then write, fsync, unmount, remount, read, and clean up its
marker before fire-fuse is restarted.

If mutation, smoke testing, or final readiness fails, the rescue path restores
all three previous files without logging their contents, restarts Redis,
requires an authenticated `PONG` using the restored password, restarts
fire-fuse, and requires `/readyz` before reporting the failed rotation. The new
Vault value remains unchanged on the controller after rollback, so ordinary
storage reconciliation will continue to fail closed until the operator either
retries this playbook or intentionally restores the prior Vault value.

Rotation rejects check mode because service shutdown, credential replacement,
rollback, and JuiceFS I/O cannot be simulated safely.

### Rotate the MinIO credential

First drain every active microVM. Preserve the current
`minio_object_juicefs_secret_key` as a separate encrypted rollback value, then
replace it with a new 64-character lowercase hexadecimal value:

```yaml
storage_rotate_minio_previous_secret_key: <previous Vault secret>
minio_object_juicefs_secret_key: <replacement Vault secret>
```

The former `fire_fuse_rotate_minio_previous_secret_key` name is still accepted
when the new variable is unset.

Run only the dedicated maintenance playbook:

```sh
ansible-playbook \
  --inventory inventory/staging/hosts.yml \
  --ask-vault-pass \
  playbooks/storage/rotate-minio-credential.yml
```

The role rejects check mode, active VMs, host JuiceFS mounts, an unavailable
previous credential, or an already-active replacement credential. It first
qualifies the current JuiceFS data path, stops fire-fuse, rotates the dedicated
MinIO user, updates the object-store credential stored in JuiceFS metadata,
reruns the host mount/write/remount smoke test, and restores `/readyz`.

If mutation, metadata update, smoke testing, or service restoration fails, the
rescue path stops fire-fuse, restores the previous MinIO credential and JuiceFS
metadata setting, qualifies the restored data path, restores fire-fuse, and
then reports the rotation failure. Remove the previous secret from active
inventory only after the successful maintenance run has been recorded.

The pinned JuiceFS release requires `config` changes to supply the object-store
secret as an explicit command option; its environment-variable credential
handling applies to initial formatting but not this update path. The rotation
tasks therefore use argument-vector invocation with `no_log: true`. The secret
is suppressed from Ansible output but is briefly present in the root process
argument vector during each update or rollback and may be observable through
`/proc` under the host's procfs policy. This is acceptable only for the current
trusted, non-multi-tenant dedicated host.

## Provision MinIO object storage

MinIO Community Edition is now distributed as source. Build the final community
security release into a Linux/amd64 artifact from the repository root:

```sh
./storage/minio/build-minio-linux-amd64.sh
```

The script performs a pinned Go module build of the release declared inside
[`build-minio-linux-amd64.sh`](../../storage/minio/build-minio-linux-amd64.sh) and
writes the binary plus its SHA-256 under `dist/`. Artifact building remains a
narrow local script; it does not mutate host state.

Add the absolute controller path and the generated 64-character checksum to a
nonsecret inventory variable file:

```yaml
minio_object_server_artifact: "/absolute/path/to/dist/minio-linux-amd64"
minio_object_server_checksum: "replace-with-generated-sha256"
```

Prepare the pinned JuiceFS client artifact on the controller as well:

```sh
./storage/juicefs/prepare-juicefs-linux-amd64.sh
```

Add its path and generated binary checksum beside the MinIO variables:

```yaml
juicefs_volume_client_artifact: "/absolute/path/to/dist/juicefs-linux-amd64"
juicefs_volume_client_checksum: "replace-with-generated-sha256"
```

The full storage playbook applies the baseline, private network, Redis, and
MinIO roles in dependency order, then formats and verifies the JuiceFS volume:

```sh
ansible-playbook \
  --inventory inventory/staging/hosts.yml \
  --ask-vault-pass \
  --check \
  --diff \
  playbooks/storage/storage.yml

ansible-playbook \
  --inventory inventory/staging/hosts.yml \
  --ask-vault-pass \
  playbooks/storage/storage.yml
```

The MinIO server source is pinned by the build script, and `mc` is pinned in
[`roles/storage/minio_object/defaults/main.yml`](roles/storage/minio_object/defaults/main.yml).
The role verifies the server artifact on the controller before copying it and
verifies the client's published SHA-256 while downloading it. Updating either
release requires reviewing its security notes and rerunning syntax, lint,
artifact, and dedicated-host qualification.

After applying, require the private API, loopback-only console, service, and
bucket to be present:

```sh
curl --fail http://10.78.0.2:9000/minio/health/ready
ss -lntp
systemctl is-active fire-fuse-minio.service
```

The role handles the MinIO password with `no_log` and disabled diffs. Run the
playbook a second time and require no changed tasks.

The first successful storage run reports one changed JuiceFS formatting task.
Later runs must not format again and must report no changes. To qualify the
volume independently after storage services are already present, run:

```sh
ansible-playbook \
  --inventory inventory/staging/hosts.yml \
  --ask-vault-pass \
  --check \
  --diff \
  playbooks/storage/juicefs-volume.yml

ansible-playbook \
  --inventory inventory/staging/hosts.yml \
  --ask-vault-pass \
  playbooks/storage/juicefs-volume.yml
```

The formatting command receives Redis and MinIO credentials only through its
environment, and all credential-bearing Ansible tasks suppress logs and diffs.
The resulting object-storage credential is stored in JuiceFS metadata by
design. The existing `/etc/fire-fuse/juicefs-meta-url` remains a root-only host
operations credential. It is not consumed by fire-fuse or delivered to guests.

## Qualify JuiceFS without Firecracker

Before installing or starting fire-fuse, exercise the formatted volume with a
normal host JuiceFS client:

```sh
ansible-playbook \
  --inventory inventory/staging/hosts.yml \
  playbooks/compute/juicefs-smoke.yml
```

The `juicefs_smoke` role mounts the volume at
`/mnt/fire-fuse-juicefs-smoke`, writes and fsyncs a reserved marker, unmounts,
remounts, verifies the exact marker contents, deletes the marker, and unmounts
again. Its exit cleanup also removes the reserved marker and unmounts after a
failure when the backend remains reachable. It refuses to run when the
mountpoint is already mounted.

The qualification playbook does not apply the storage roles or read the
controller's Vault values. It fails unless the role-pinned JuiceFS client,
root-only metadata URL, Redis, and MinIO are already usable, so a passing
result cannot be caused by repairing or reformatting the backend immediately
before the test.

This is an active qualification, so it intentionally reports a changed task on
every non-check run even though it leaves no file in the volume. Check mode
previews the supporting configuration but skips FUSE and the storage exercise;
it is not evidence that persistence works.

## Stage the fire-fuse runtime release

Build the fire-fuse Linux binaries, including `fire-fuse-snapshot-linux-amd64`,
and prepare the guest artifacts as described
in the Hetzner staging guide. Prepare the Firecracker and jailer pair from the
checksum-pinned official release selected by
[`prepare-firecracker-linux-amd64.sh`](../../scripts/prepare-firecracker-linux-amd64.sh):

```sh
./scripts/prepare-firecracker-linux-amd64.sh
```

The script verifies the complete x86-64 release archive before extracting only
the Firecracker and jailer binaries, checks that both are Linux x86-64 ELF
files, and writes their individual SHA-256 files under `dist/`.

Record absolute source paths and lowercase SHA-256 values in the nonsecret
inventory variables:

```yaml
fire_fuse_stage_orchestrator_artifact: "/absolute/path/to/dist/fire-fuse-linux-amd64"
fire_fuse_stage_orchestrator_checksum: "replace-with-sha256"
fire_fuse_stage_admin_artifact: "/absolute/path/to/dist/cantelop-linux-amd64"
fire_fuse_stage_admin_checksum: "replace-with-sha256"
fire_fuse_stage_snapshot_builder_artifact: "/absolute/path/to/dist/fire-fuse-snapshot-linux-amd64"
fire_fuse_stage_snapshot_builder_checksum: "replace-with-sha256"
fire_fuse_stage_image_publisher_artifact: "/absolute/path/to/dist/fire-fuse-image-linux-amd64"
fire_fuse_stage_image_publisher_checksum: "replace-with-sha256"
fire_fuse_stage_integration_artifact: "/absolute/path/to/dist/fire-fuse-integration-linux-amd64"
fire_fuse_stage_integration_checksum: "replace-with-sha256"
fire_fuse_stage_firecracker_artifact: "/absolute/path/to/dist/firecracker-linux-amd64"
fire_fuse_stage_firecracker_checksum: "replace-with-sha256"
fire_fuse_stage_jailer_artifact: "/absolute/path/to/dist/jailer-linux-amd64"
fire_fuse_stage_jailer_checksum: "replace-with-sha256"
fire_fuse_stage_kernel_artifact: "/absolute/path/to/artifacts/vmlinux"
fire_fuse_stage_kernel_checksum: "replace-with-sha256"
fire_fuse_stage_rootfs_artifact: "/absolute/path/to/artifacts/ubuntu-24.ext4"
fire_fuse_stage_rootfs_checksum: "replace-with-sha256"
fire_fuse_stage_host_artifact_names:
  - guest rootfs
fire_fuse_release_images:
  - name: "application"
    rootfs_artifact: "/absolute/path/to/artifacts/application.ext4"
    rootfs_checksum: "replace-with-sha256"
    process_artifact: "/absolute/path/to/artifacts/application.process.json"
    process_checksum: "replace-with-sha256"
```

Artifacts listed by name in `fire_fuse_stage_host_artifact_names` already exist
on the compute host at their declared absolute paths. This is intended for
large guest and application root filesystems built on that host. Other inputs
remain on the Ansible controller. Staging hashes each source where it lives,
copies host-local inputs within the host, then verifies every staged copy
against its declared checksum. To stage a host-built application image, add
`container image application` to the list. The rootfs is no longer read back
through the controller to derive its Build ID; the source `stat` supplies the
verified digest and byte size.

`prepare-guest.sh` emits `.sha256` sidecars for the three guest artifacts and
the fixture process contract. `prepare-docker-image.sh` likewise emits
sidecars for the application ext4 image and process JSON. Review those
lowercase digests and copy them into the untracked local inventory or the
inventory used by a remote controller. Staging independently hashes each
actual input and rejects a declared value that does not match.

Staging also hashes the canonical process object, records the rootfs byte size,
and derives the final `sha256:<digest>` Build ID. That generated entry is
written into the separate image catalog; operators do not supply a separate
Build ID. Existing trusted catalog entries are merged into the proposed
catalog, so a later runtime release does not retire images that were published
independently. The
rootfs is installed at the derived immutable path
`/var/lib/fire-fuse/images/sha256-ROOTFS_DIGEST.ext4` and checked against both
its reviewed checksum and generated catalog size before the release can be
promoted. Operators cannot select a mutable destination. Promotion refuses an
existing digest path whose bytes do not match instead of overwriting it.

The process artifact must be exactly one JSON object with `args`, `env`,
`working_directory`, `uid`, and `gid`. Staging rejects extra fields, invalid
types, empty commands, and identities outside the Linux `uint32` range before
rendering the host profile. Integral JSON numeric identities are normalized to
integer literals. This keeps Docker image inspection/materialization and
deployment connected by one checksum-reviewed contract.

Preview and stage the release:

```sh
ansible-playbook \
  --inventory inventory/staging/hosts.yml \
  --ask-vault-pass \
  --check \
  --diff \
  playbooks/compute/stage-runtime.yml

ansible-playbook \
  --inventory inventory/staging/hosts.yml \
  --ask-vault-pass \
  playbooks/compute/stage-runtime.yml
```

The role requires the Ansible-managed root-only JuiceFS metadata URL to exist
before rendering workspace configuration. Inspect
`/var/lib/fire-fuse/staged-release/config.json`, `image-catalog.json`, and
`release.json`, then rerun the playbook and require no changes. Do not manually
copy staged files into their live destinations; promotion, service migration,
preflight, and rollback are intentionally deferred to the next reviewed
checkpoint.

The promotion role uses the staged orchestrator's `-check-state-only` mode after
installing the allowlisted files. That mode validates the live configuration,
host contract, and lifecycle state, applying only explicitly enumerated
transactional schema migrations. It does not perform runtime recovery or start
an HTTP listener.

## Install the inactive service gate

Install the reviewed packaged systemd unit without promoting a release or
allowing fire-fuse to start:

```sh
ansible-playbook \
  --inventory inventory/staging/hosts.yml \
  --check \
  --diff \
  playbooks/compute/prepare-service.yml

ansible-playbook \
  --inventory inventory/staging/hosts.yml \
  playbooks/compute/prepare-service.yml
```

The `fire_fuse_service` role refuses to replace a running service. It installs
the repository's exact packaged unit, reloads systemd only when that file
changes, and explicitly leaves `fire-fuse.service` stopped and disabled. It
then verifies the installed checksum, ownership, mode, and inactive state.

This checkpoint deliberately does not copy staged runtime files to their live
destinations, run preflight, enable the service, or open the HTTP listener.
Those mutations remain separate reviewable steps.

## Prepare a production runtime upgrade

After a clean shutdown and before promotion, retain the exact current release
and its state under a unique upgrade ID:

```sh
export CANTELOP_FIRE_FUSE_PREPARE_UPGRADE='PREPARE FIRE-FUSE PRODUCTION UPGRADE'
release_revision=$(git rev-parse --short=12 HEAD)
export CANTELOP_FIRE_FUSE_UPGRADE_ID="$(date -u +%Y%m%dT%H%M%SZ)-${release_revision}"
ansible-playbook --inventory inventory/staging/hosts.yml \
  playbooks/compute/prepare-runtime-upgrade.yml
unset CANTELOP_FIRE_FUSE_PREPARE_UPGRADE
```

The role requires Fire Fuse to be stopped and disabled, rejects SQLite
sidecars, and copies the fixed host release allowlist, lifecycle database, and
configuration-encryption key into a root-only create-only directory beneath
`/var/lib/fire-fuse/release-rollbacks`. It writes SHA-256 evidence and verifies
every copy. This is the rollback boundary for a runtime upgrade; it is separate
from the shared workspace backup contract. Keep
`CANTELOP_FIRE_FUSE_UPGRADE_ID` set through promotion: promotion verifies that
this snapshot's lifecycle database is byte-for-byte identical to the live
pre-migration database.

When a reviewed release requires an offline state cutoff, preserve this
pre-cutoff snapshot first, transform and validate the stopped database, then
bind promotion to the exact post-cutoff checksum:

```sh
export CANTELOP_FIRE_FUSE_STATE_CUTOVER='PROMOTE OFFLINE FIRE-FUSE STATE CUTOVER'
export CANTELOP_FIRE_FUSE_STATE_CUTOVER_SHA256='POST_CUTOFF_STATE_DB_SHA256'
```

The retained database remains bound to its pre-cutoff checksum so rollback
restores the prior binary and schema together. Unset both cutoff variables
after promotion.

If activation or smoke qualification requires rollback, stop and disable Fire
Fuse, select the retained ID, and use the separately approval-gated playbook:

```sh
export CANTELOP_FIRE_FUSE_ROLLBACK='RESTORE FIRE-FUSE PRODUCTION ROLLBACK'
export CANTELOP_FIRE_FUSE_UPGRADE_ID='RETAINED_UPGRADE_ID'
ansible-playbook --inventory inventory/staging/hosts.yml \
  playbooks/compute/rollback-runtime.yml
unset CANTELOP_FIRE_FUSE_ROLLBACK CANTELOP_FIRE_FUSE_UPGRADE_ID
```

Rollback verifies the strict manifest, restores only its fixed allowlist, and
requires the restored binary to pass `-check-state-only`. Rollback deliberately
leaves the service stopped and disabled. Activation repeats lifecycle-state
validation as an explicit later checkpoint. Retain the snapshot until the
approved rollback window closes; no automatic deletion is provided.

## Promote and preflight the runtime

After reviewing the staged manifest and preparing the inactive service gate,
promote the release without starting fire-fuse:

```sh
ansible-playbook \
  --inventory inventory/staging/hosts.yml \
  playbooks/compute/promote-runtime.yml
unset CANTELOP_FIRE_FUSE_UPGRADE_ID
```

The `fire_fuse_promote` role accepts only the fixed base runtime destinations,
the digest-derived image destinations declared by `fire_fuse_release_images`,
`/etc/fire-fuse/image-catalog.json`, and `/etc/fire-fuse/config.json`; paths
supplied by the staged manifest cannot expand that allowlist. Release-manifest
schema 3 records the catalog and configuration as separate transactional
metadata with independent SHA-256 values, plus the exact live-catalog digest
merged during staging. Promotion verifies those checksums, ownership, and modes
and refuses untrusted existing live targets. A
digest-derived image path is create-only: if it already contains different
bytes, promotion fails rather than replacing it.

Promotion preserves `/var/lib/fire-fuse/state/state.db` when it exists and
requires it to be a regular file. A state-bearing promotion also requires the
upgrade ID created by `prepare-runtime-upgrade.yml`; the role verifies the
manifest and requires the retained database checksum to match the live database
before changing files. An explicitly approved offline cutoff instead requires
the live checksum to match `CANTELOP_FIRE_FUSE_STATE_CUTOVER_SHA256`, while the
rollback copy remains bound to the manifest's pre-cutoff checksum. The promoted
runtime then validates the exact recorded schema and host prerequisites with
`-check-state-only`. Unsupported or structurally unexpected schemas fail closed.

Immediately before changing any live target, promotion takes the image catalog
writer lock, compares the live catalog to the staged base digest, and creates a
root-only deployment lease. If an image was published or retired after staging,
the digest comparison fails and the release must be staged again; promotion
never overwrites that catalog change. The lease remains present through copy,
preflight, and any rollback, causing `fire-fuse-image` publish, retire, and
collection operations to fail without mutation. The role releases the lease in
its unconditional cleanup path.

Only targets whose content or mode differs are backed up and replaced. Each
replacement uses Ansible's atomic copy path while the service is stopped and
disabled. The newly promoted binary runs `-check-state-only` against the live
configuration and lifecycle database. If copying or validation fails, the role restores replaced
files, removes targets that did not previously exist, and reports failure.

An unchanged release still runs preflight but reports no promotion changes.
Successful promotion leaves the service stopped and disabled; activation and
the `/readyz` check remain a later checkpoint.

An ungraceful controller or host failure can leave
`/etc/fire-fuse/image-catalog.json.lock.deployment` behind. Do not remove it
while a promotion process is running. After confirming promotion is no longer
running and fire-fuse remains stopped, remove only that lease file, then rerun
staging and promotion from the beginning. The live catalog must not be copied
or edited as part of lease recovery.

Promotion intentionally rejects Ansible check mode because its private backup
directory and rollback behavior cannot be simulated safely. Use the isolated
staged release, manifest, and `stage-runtime.yml --check --diff` output for
review before running this transactional playbook.

### Initialize and validate lifecycle state

The lifecycle database is created when missing and records schema version 20.
On restart, Fire Fuse accepts exact version 20 or transactionally migrates
supported versions 4 through 19. The migration chain preserves the Workspace,
reusable Session, Session work-lease, and encrypted Message delivery
projections and is idempotent after commit. Unknown versions and unexpected
table, column, or index shapes fail closed. Every future schema
change must add and qualify another explicit transactional migration before
release.

## Build and enable the runtime snapshot

Set `fire_fuse_stage_snapshot_bundle` and `fire_fuse_snapshot_bundle` to the
same new, unique bundle path before staging. Stage the complete release once,
then build the bundle from its verified staged builder, Firecracker, jailer,
kernel, and platform rootfs:

```sh
ansible-playbook \
  --inventory inventory/staging/hosts.yml \
  playbooks/compute/build-runtime-snapshot.yml
```

The bundle must be a unique child of
`/var/lib/fire-fuse/snapshots`, such as
`/var/lib/fire-fuse/snapshots/default-4vcpu-512m-hotplug-4g-RELEASE`. The role creates a
root-owned 64 MiB sparse placeholder only when absent and publishes that exact
bundle. It uses a separate runtime directory so the active service can remain
up while the snapshot is built. It refuses check mode, untrusted staged inputs,
an untrusted existing path, or any attempt to replace a bundle. It verifies the
manifest against the state and memory file hashes and requested VM dimensions.
Rerun the playbook and require no changes.

The generic snapshot target starts the platform guest's `ssh.service` alongside
the assignment agent. This is an operator qualification channel rooted in the
platform image and its embedded client authorization and server host keys; it
is not the customer container process. The server keys identify this immutable
qualification fixture rather than an individual restored VM, and the private
integration client disables host-key persistence. Customer workloads remain
constrained to their declared non-root UID and GID. Production builds must
create a new immutable snapshot bundle whenever the platform guest target
changes.

After the bundle is verified, drain Sessions, stop and disable fire-fuse, and
run `promote-runtime.yml` once. Promotion executes the new binary's full
preflight against the bundle before accepting the config and
retains its normal transactional rollback. Requests for exactly four vCPUs and
512 MiB boot memory with a 4096 MiB hotplug range use restore; every other shape is rejected before lifecycle state is
created.

The Sandbox CPU and memory model is in
[`docs/sandbox-resources.md`](../../docs/sandbox-resources.md).

At 50 slots, 25 GiB of the 50 GiB (`51200` MiB) aggregate limit is reserved
for boot memory; up to 25 GiB is available for hotplug growth. Each VM has a
512 MiB boot size and a 4096 MiB hotplug ceiling. On the 64 GB staging host
that leaves roughly 12 GiB for services, the JuiceFS gateway, page cache, and
VMM overhead, so verify host headroom. Qualify all 50 slots under load,
checking concurrent growth, shrink, guest memory, startup latency, host memory
pressure, and OOM counters; an idle capacity check alone does not qualify the
new snapshot.

## Activate fire-fuse

After a successful promotion, activate the service and require its real
readiness endpoint:

```sh
ansible-playbook \
  --inventory inventory/staging/hosts.yml \
  playbooks/compute/activate-runtime.yml
```

The `fire_fuse_activate` role accepts only the fixed unit, promoted binary,
configuration paths, and `127.0.0.1:8080` API endpoint. It verifies file trust,
requires either the initial stopped/disabled state or the final running/enabled
state, reruns `-check-state-only`, and validates the installed unit with
`systemd-analyze verify` before activation.

It then enables and starts fire-fuse and retries `GET /readyz` for up to 30
seconds. Enabled gateway, observer, and durable control-worker services stopped
with Fire Fuse are restored only after readiness succeeds. A failed first
activation restores the prior stopped/disabled service state. An idempotent
repeat starts from running/enabled and preserves that prior state if its
readiness check fails.

Activation rejects check mode because neither process startup nor HTTP
readiness can be simulated. It does not create a VM or run the cross-VM
persistence suite.

## Qualify the activated runtime

Set the absolute path to the private half of the SSH key embedded in the guest
image:

```yaml
fire_fuse_qualify_ssh_key: /root/.ssh/id_ed25519
fire_fuse_qualify_build_id: sha256:replace-with-active-catalog-build-id
```

Then run the production-realistic suite:

```sh
ansible-playbook \
  --inventory inventory/staging/hosts.yml \
  playbooks/compute/qualify-runtime.yml
```

The qualification role first requires the loopback API to be ready and refuses
to continue if any microVM is starting, running, or stopping. It stops the
production supervisor so the isolated integration process can safely use the
same jail, TAP, firewall, and address namespaces. Its `always` section restarts
fire-fuse and requires `/readyz` even when an integration assertion fails.

To check elastic memory through a live App and gateway Session after
activation, build the test helper from the repository root as a static Linux
amd64 binary:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /tmp/elastic-memory-pressure ./tests/live/elastic-memory-pressure
```

Copy that binary and `tests/live/check-elastic-memory.py` to the compute host.
Run the script there as root with the active App, Workspace, hostname, and
guest qualification key:

```sh
python3 /root/check-elastic-memory.py \
  --app-id app_REPLACE --workspace-id wsp_REPLACE \
  --hostname app.example.com \
  --pressure-binary /root/elastic-memory-pressure \
  --guest-key /root/fire-fuse-upload/fire-fuse-guest-test-key
```

The script creates a temporary Session with a work lease, checks the live App
runtime endpoint, drives the guest from 512 MiB to at least 768 MiB, waits for
it to return to 512 MiB, and removes its Session and guest helper in cleanup.
Use a staging App and Workspace; the test briefly consumes a Sandbox slot and
up to 768 MiB of guest memory.

The Build ID must be an exact entry in the active content-addressed catalog;
the integration suite reads vCPU and memory from the profile's snapshot
manifest. The default run covers Session lifecycle, cross-VM JuiceFS
persistence, Firecracker-process crash, and orchestrator restart recovery.
Enable the hung-unmount failure injection explicitly:

```yaml
fire_fuse_qualify_unmount_hang: true
```

Storage-host outage qualification is a separate maintenance operation. The
runtime qualification no longer stops Redis or MinIO because those services
run on `storage_hosts`. The hung-unmount injection modifies only an ephemeral
guest rootfs.

Qualification rejects check mode because real Firecracker processes and
failure injection cannot be simulated. This playbook is intentionally not part
of deployment and should run only during an explicit maintenance window on the
dedicated staging host.

### Measure the storage baseline

The benchmark is an opt-in host qualification, not a deployment step. Configure
an immutable Build, the guest SSH key, the controller output directory, and an
explicit confirmation:

```yaml
fire_fuse_benchmark_confirm: true
fire_fuse_benchmark_trials: 3
fire_fuse_benchmark_build_id: sha256:replace-with-active-catalog-build-id
fire_fuse_benchmark_ssh_key: /root/.ssh/id_ed25519
fire_fuse_benchmark_ssh_port: 22
fire_fuse_benchmark_runtime_port: 3000
fire_fuse_benchmark_output_directory: /absolute/controller/result/path
fire_fuse_benchmark_concurrency: 3
fire_fuse_benchmark_policy: /absolute/controller/path/policy.json
```

Run:

```sh
ansible-playbook \
  --inventory inventory/staging/hosts.yml \
  playbooks/compute/benchmark-storage.yml
```

Each trial creates a canonical App and Workspace for the selected
Build, measures the internal Sandbox lifecycle over the loopback operator API,
and archives the App during cleanup. Historical result schema and policies are
retained so new series remain comparable with existing evidence. See
[`docs/storage-performance.md`](../../docs/storage-performance.md) for the
measurement boundary, evidence format, summarizer, and warmup gate.

For the separate five-axis capacity benchmark (CPU load, memory load,
Workspace I/O, startup throughput and startup time), use
[`tests/benchmarks/README.md`](../../tests/benchmarks/README.md#five-axis-host-capacity-benchmark).
