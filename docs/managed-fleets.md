# Managed application Fleets

Fleets let a service own many isolated Apps without asking its customers to hold
Cantelop accounts. A Fleet belongs to one Cantelop user. Machine principals are
scoped to that Fleet; they are not general customer/administrator tokens.

Packages built with `fleet_execution_protocol_version: 1` publish their Edge
code once and reuse their source release's prepared runtime image. Each
installation runs in a separate on-demand Edge isolate with its own bindings,
credentials and fenced runtime access. The compute host verifies and reuses
installed image bytes. Existing packages without that SDK contract keep their
legacy deployment path until replaced by a compatible package. See
[shared Fleet packages](architecture/fleet-code-sharing.md).

## Control API

All responses are private and non-cacheable. Owner actions use an ordinary
Cantelop owner token with `tenant:write`. Machine actions use the one-time
`fsp_<id>.<secret>` credential returned when creating a principal. Only its hash
is stored. Do not write credentials to logs or commit them.

| Method/path under `/v1/fleets` | Authority | Result |
| --- | --- | --- |
| `GET /`, `POST /` with `{name,maxApps}` | Owner | List/create Fleets |
| `PATCH /:fleet` with `{maxApps}` | Owner | Change the installation cap; `null` means unlimited |
| `GET /:fleet/principals` | Owner | Metadata, scopes, revocation/expiry |
| `POST /:fleet/principals` with `{label,scopes,expiresAt}` | Owner | One-time secret, max 366-day lifetime |
| `DELETE /:fleet/principals/:principal` | Owner | Revoke immediately |
| `POST /:fleet/packages` with `{releaseId,version}` | `packages:write` | Immutable approved artifact pair |
| `GET /:fleet/packages/:package` | `installations:read` | Package/runtime metadata |
| `DELETE /:fleet/packages/:package` | `packages:write` | Disable future deployment |
| `PUT /:fleet/installations/:externalKey` with `{packageId}` | `installations:write` | Idempotent installation intent |
| `GET /:fleet/installations?externalKey=...` | `installations:read` | Recover an ambiguous creation without creating anything |
| `DELETE /:fleet/installations?externalKey=...` | `installations:write` | Permanent external-key tombstone, serialized with in-flight creation |
| `GET /:fleet/installations/:id` | `installations:read` | Release, runtime, policy revisions, lifecycle status |
| `GET /:fleet/installations/:id/credentials` | `credentials:read` | Server-only App credential |
| `PUT /:fleet/installations/:id/policy` | `policy:write` | `{expectedRevision,policy,sourceFence?}` optimistic/fenced update |
| `PUT /:fleet/installations/:id/budget` | `policy:write` | Immutable `{epoch,included,expiresAt}` integral grant |
| `GET /:fleet/installations/:id/budget?epoch=...` | `usage:read` | Included, settled, reserved, available credits |
| `POST /:fleet/installations/:id/releases` | `installations:write` | `{packageId,expectedPackageId}` drain/upgrade intent |
| `DELETE /:fleet/installations/:id` | `installations:write` | Stop, archive and purge intent |
| `GET /:fleet/usage?after=0` | `usage:read` | Monotonic settlement cursor, at most 500 records |

`policy` contains `active`, `expiresAt`, `maxSandboxes`, aggregate `maxVcpus`,
aggregate `maxMemoryMib`, `maxWorkspaces`, and `maxStorageBytes`. Memory includes
boot memory plus the full configured hotplug capacity, not host RSS. Shape and
Workspace limits are enforced in compute as well as the control plane.

Each Fleet counts as **one App slot** against its owner's account quota, reserved
when the Fleet is created, even while empty. Installation Apps consume no
additional account slots. Standalone Apps, including a package's source App,
each consume their own slot. Ordinary App creation and Fleet creation share the
same serialized quota check. Migration `0031_fleet_app_slots` raised any
existing account quota this accounting would exceed, so no account is over
quota when it deploys.

Set `maxApps` to `null` when creating a Fleet to allow unlimited
installations; omitting it is rejected with `invalid_limit`. A finite cap must
be an integer from 1 through 10,000. Existing
Fleets retain their configured caps; owners can set `maxApps` to `null` with
`PATCH /v1/fleets/:fleet` to remove a cap. Lowering a cap blocks new installations
until usage drops below it; it does not delete existing Apps. Deleting Apps with
pending purge still occupy finite Fleet capacity. Resource policies, storage
quotas and funded execution budgets apply equally to unlimited Fleets.

For Juiced, issue separate principals:

- API: `installations:read`, `credentials:read`.
- Worker: `installations:read`, `installations:write`, `credentials:read`,
  `policy:write`, `usage:read`.
- Release publisher: `packages:write`, `installations:read`.

Publish a successfully deployed, owned source release. Publishing snapshots its
validated Edge/runtime artifact IDs and compiled runtime identity. Installation
uses a random physical App slug, independent credentials and an atomic binding
between App, installation and package. Source environment values and credentials
are not copied. Ordinary deployment name checks remain unchanged. The explicit
package path accepts only the stored, approved binding.

For a compatible package, publication snapshots the source release's verified
prepared-runtime receipt and asynchronously copies its verified Edge bundle
into immutable package storage. `GET /:fleet/packages/:package` reports
`execution: preparing` until that copy is acknowledged, then `ready`. Legacy
packages report `legacy`. An initial installation may wait for preparation;
an upgrade to a preparing package returns `package_preparing` before draining
the current runtime. Publication retries once per package, independently of
installation count.

Installation releases use `attach_package` operations. They attach the prepared
Build through the existing authorized compute endpoint and select package code
behind the App's release gate. They never run a per-installation Edge upload or
Docker conversion. Current configuration is projected to the installation's
gate; configuration revisions and activation generations fence stale runtime
capabilities. Streams and WebSockets close on a configuration or release
transition. Archival removes gate secrets and permanently retires its App
identity while retaining shared package assets. Package storage is retained
indefinitely; disabling a package prevents future assignments without deleting
assets needed by existing installations or rollback.

Roll out migration `0032_shared_fleet_packages`, the updated dispatcher and
compute service, and the control worker before publishing compatible packages.
The dispatcher adds a `FleetPackage` Durable Object migration, Worker Loader and
the existing origin broker service binding; it reuses the release-gate and
observability credentials. Rebuild source releases with the SDK Fleet transport
contract, publish a new package version, and move installations through the
existing release API. Existing package versions remain immutable. Follow the
[coordinated deployment runbook](deployment.md), including public source
publication/pinning and SDK release qualification.

Deleting by external key records a permanent tombstone under the same lock as
creation, including when an ambiguous creation has not returned an App ID. A
late creation cannot resurrect that intent. Managed services can send a monotonic
`sourceFence` with policy updates; an older job cannot undo a newer suspension
even after its database connection/lock was lost.

The control worker reconciles installation/release intent and policy/budget
projection. `enforced_revision` advances only after host acknowledgement. Upgrade
intent closes managed admission, waits for all old VMs to drain, then deploys the
new package through the existing release/activation machinery. Rollback selects
an older retained package using the same path. Application schema compatibility
and retention rules still apply.

## Dedicated managed compute host

Managed Apps are denied from the transaction that first registers them on a
host. Before enabling them, provision a **dedicated** host with the normal
validated Fire Fuse release and JuiceFS/NFS setup, plus these additions:

1. Build/install `fire-fuse-managed-watchdog` from the same release bundle. The
   release schema is now SQLite **28**, with additive upgrades from 24–27.
2. Install `platform/systemd/compute/fire-fuse-managed-watchdog.service`. Its
   `-state` path must equal runtime's configured SQLite path, and `-cgroups` must
   identify the actual Fire Fuse service's `sandboxes` cgroup subtree. Keep the
   watchdog outside the runtime process and cgroup; it needs root access to write
   `cgroup.kill` and read SQLite/WAL. Its own systemd watchdog restarts a frozen
   watchdog process.
3. Install the managed drop-in from
   `platform/systemd/compute/fire-fuse.service.d/managed.conf.example`, and a root
   `0600` `/etc/fire-fuse/managed-storage.env` containing
   `CANTELOP_MANAGED_JUICEFS_META_URL` and, if mounting below the filesystem root,
   `CANTELOP_MANAGED_JUICEFS_PREFIX`. Never print the metadata URL in diagnostics.
4. Start the watchdog before runtime, then verify its private heartbeat becomes
   `ready`. Managed host configuration refuses existing unmanaged Apps and new
   unmanaged registrations. Do not apply this drop-in to a shared legacy host.
5. Apply control-plane migrations and restart the control worker with the correct
   compute ingress. Run bounded canaries before increasing capacity.

A policy requires functioning JuiceFS hard quotas. Each Workspace reserves its
share before registration; files receive the allocation minus 256 MiB reserved
for its Turso database. File limits round down to whole GiB, so an active policy
requires at least 1.25 GiB per permitted Workspace. Database size limits are set
and read back before workload tokens are issued. Purge stops compute and
deletes Workspace files before releasing the host's retained storage
allocation, then deletes the database.

## Funded execution and failure behavior

The integral rate card is `vCPU * 1024 + maximum guest MiB` credits per second.
Every execution window is prepaid from an immutable allowance epoch. The central
ledger transactionally maintains `settled + reserved <= included`; twenty
concurrent requests cannot each spend the same remaining allowance.

A host grant lasts at most 60 seconds (the reconciler currently requests 45).
Its identity binds to the host's OS boot identity rather than a reusable ingress
URL. Admission is atomic with Sandbox creation and checks aggregate resource
limits. A separate timer meters allocated capacity, persists accounting and
terminates VM processes before the funded deadline. Idle windows and work leases
cannot extend funding. Policy suspension and limit reductions terminate VM
cgroups/VM processes rather than merely aborting an SDK request.

The independent watchdog reads committed policy and Sandbox state. It kills VM
cgroups if a grant expires, runtime's heartbeat stalls, the clock moves backwards,
or accounting cannot be read. Runtime admission also requires a healthy watchdog
heartbeat. On runtime restart, old grants are conservatively consumed and native
compute is stopped before listeners open. Runtime and watchdog failure paths
must be qualified on Linux/KVM; unit tests use disposable SQLite and cgroup
fixtures.

Final usage is written durably before acknowledgement. The control worker settles
it idempotently and only then acknowledges the outbox. Finalized grant IDs remain
tombstoned after acknowledgement, preventing replay. An uncertain reservation is
never released just because it expired. An unrecoverable host can conservatively
consume the full reservation after operators establish that its native compute
is dead; do not manufacture a refund from a missing heartbeat.

Current placement uses the configured compute ingress. A replacement boot needs
fresh reservations; reconcile old evidence and stop the old host before moving
work. Monitor lease renewal latency, policy lag, watchdog freshness, stale
reservations, principal expiry and purge failures. Keep admission closed during
restores, reconcile ledger/host evidence, and never reset settled usage to make a
restore succeed. Juiced documents the service-side restore and revocation steps.
