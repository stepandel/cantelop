# External product control plane

Cantelop separates its product control plane from the compute runtime. The
console stack owns customer-facing identity and desired state in PostgreSQL;
fire-fuse is a private compute agent whose durable state describes one host.

```text
Console and CLI
      |
      v
Next.js control API ------> PostgreSQL
      |                         |
      |                         +-- users and quotas
      |                         +-- Apps and Session runtimes
      |                         +-- Workspaces
      |                         +-- durable control operations
      |
      +--> private object storage <--- direct CLI uploads
      |
      +--> control/build VM --> API provider
                 |                 |
                 |                 +--> R2 prepared ext4 images
                 +--> Fire Fuse over WireGuard
      |
      +--> compute adapter --> fire-fuse --> Sessions
```

PostgreSQL is authoritative for opaque product IDs, ownership, globally
routable App slugs, configuration, desired deployments, and operation status.
Large artifacts belong in object storage and are referenced by immutable ID;
they are never stored in PostgreSQL or proxied through normal Next.js request
bodies.

Fire-fuse remains authoritative for its verified local image catalog, host slots, VM
processes, TAP/IP allocation, cgroups, local Workspace mounts, Session
state, and command idempotency. It must not connect to the product database.
Communication crosses a versioned private compute-provider contract.

## Source boundaries

The first implementation remains in this repository and deployment stack:

- `console/core/domain` contains provider-independent product rules;
- `console/core/db/schema.ts` declares the PostgreSQL schema with
  Drizzle, while its `drizzle` directory contains generated migrations;
- `console/core/db` also contains repositories for transactional
  product operations;
- provider adapters consume durable operations without entering the
  domain or database modules;
- Next.js route handlers authenticate and translate HTTP but do not contain
  SQL or long-running deployment logic.

This is a module boundary rather than a claim that alternative providers
already exist. Current operation kinds name real workflows, and provider
interfaces will be introduced only where a real boundary is exercised.

Repositories use Drizzle's typed query builder for ordinary projections and
state transitions. PostgreSQL-specific concurrency primitives remain explicit
SQL where that makes the invariant visible: advisory migration locking,
aggregate admission locks, queue claiming with `FOR UPDATE SKIP LOCKED`, stale
operation recovery CTEs, and serialized release activation.

## PostgreSQL contract

Generate and review a migration whenever the declarative schema changes:

```sh
cd console
bun run db:generate
```

Apply checked-in migrations before starting a new console release:

```sh
cd console
export CANTELOP_DATABASE_URL=postgresql://cantelop:secret@db.example.com/cantelop
export CANTELOP_DATABASE_SSL=verify-full
bun run db:migrate
```

Verified TLS is the default and the connection URL is normalized to
`sslmode=verify-full`. Disabling TLS is accepted only for a numeric or named
loopback database used during development. Drizzle Kit owns migration
generation and metadata; the Drizzle migrator applies them transactionally.
The command also takes a PostgreSQL advisory lock so concurrent release jobs
cannot race migration application.

Drizzle migrations can run in CI/CD in the same way as Prisma migrations. A
CI release job may run `bun run db:migrate` before `bun run build`; Drizzle
reads the checked-in journal, applies only pending SQL files, and records them
in `cantelop_control.__drizzle_migrations`. This is explicit pipeline wiring,
not a Vercel- or ORM-specific automatic hook. Preview deployments must use an
isolated database or skip the production migration step. Cantelop keeps the
host migration in the coordinated release workflow because both Vercel and the
control/build VM worker consume the same schema.

Checked-in PostgreSQL migrations are deployment history, not an application
compatibility surface. Once a migration has shipped, keep its SQL, snapshot,
and journal entry immutable: the checksum-addressed host bundle needs the
ordered history for existing databases, and a new database needs it to reach
the current schema. Replacing that history with a new baseline requires a
separate maintenance operation that inventories every database, proves they
already contain the baseline schema, updates Drizzle bookkeeping, and
qualifies both a fresh install and the coordinated host deployment. Do not
squash or delete migrations as ordinary dead-code cleanup.

The schema owns users, quotas, Apps, Session runtimes, Workspaces,
unified releases, upload sessions, and operations. Aggregate creation locks quota admission and
creates all initial rows in one transaction. Operation enqueueing locks the
owned App, applies a caller-supplied idempotency key, and stores bounded JSON
metadata. Workers claim ready operations with `FOR UPDATE SKIP LOCKED` and may
complete only work locked by their own worker identity.

## Implementation phases

1. **Product foundation — complete.** PostgreSQL schema, migrations, domain
   validation, aggregate repositories, and durable operations.
2. **Public control API — complete.** Resolve NextAuth identities locally and
   serve ownership-scoped App and Workspace APIs from the console origin.
   Browser routes call the same product service directly. CLI access and
   refresh tokens are issued and verified by this stack.
3. **Release API component — complete.** Verify immutable staged modules by
   digest, persist their release metadata in PostgreSQL, and run Cloudflare
   uploads from durable background operations.
4. **Unified release ingress — complete.** The CLI uploads the API module and
   multipart Session runtime archive directly to private object storage, then finalizes
   one release. PostgreSQL creates both durable child operations, and the App
   activates the release only after both succeed.
5. **Compute provider contract — complete.** App and Workspace creation
   atomically queue provider desired state containing the existing product IDs.
   The control worker registers that state idempotently, submits Session runtime
   materialization, and observes Fire Fuse's existing durable Build operation.
   App archival converges to desired absence and waits for any in-flight App
   registration before deleting provider state.
6. **Build and configuration ownership — complete.** Unified releases and
   object storage own product Build metadata and artifact ingress. PostgreSQL
   owns App variables, configuration revisions, and AES-256-GCM encrypted
   secrets; customer APIs no longer mutate Fire Fuse directly. Durable,
   revisioned `sync_config` operations replace the complete customer binding
   set in Cloudflare and the encrypted runtime cache in Fire Fuse. Projections
   are serialized per App, rechecked against current PostgreSQL state before
   completion, and reject stale host-cache revisions.
7. **Runtime-only fire-fuse — complete.** Fire Fuse no longer holds
   Cloudflare credentials, projects Worker bindings, or accepts tenant
   configuration mutations. It accepts only revisioned, full configuration
   projections through the private compute-agent API. Its public tenant binary,
   nginx site, and host verification key have been removed. Identity resolution,
   user administration, tenant CRUD/read facades, and direct streaming Build
   upload have also been removed from the private host API. SQLite stores only
   an opaque compute-tenant ownership key plus App, Workspace, Build,
   configuration-cache, and runtime projections. It stores no authentication
   identity, email, account status, product quota, or administration state.

Because there is no production state, these phases replace the old authority
directly. They do not include dual writes, data backfills, or compatibility
migrations.

The provider-local SQLite schema is version 21. Fire Fuse rejects every earlier
version; older non-production hosts must recreate their state database and replay
desired state from PostgreSQL.

Fire Fuse no longer publishes a tenant API. Customer runtime telemetry uses
the console's observer client and filters its snapshot, per-Workspace request
queries, and payload-free Message lifecycle queries against PostgreSQL
ownership before returning data. Session
termination is the sole mutation on that bridge, uses a distinct server-only
credential, and is allowed only after the console verifies App ownership from
the filtered snapshot. New deployments use the external control plane's
unified upload and release endpoints and the private Session runtime provider adapter.

Run the background control worker as a separate process from the web service:

```sh
cd console
bun run control:worker
```

The release packages that process as a standalone Bun Linux binary and installs
it on the separate control/build VM with `playbooks/builder/control-worker.yml`. The service has no HTTP
listener. Its systemd preflight validates configuration, verifies PostgreSQL
and private artifact-bucket connectivity, and its deployment role qualifies
authenticated compute ingress on `10.77.0.1:8084` over WireGuard. Vercel runs only the
request-bound Next.js control API.

`--once` checks each supported operation handler once. A continuous worker
reclaims locks older than five minutes and runs `register_app`,
`register_workspace`, `archive_workspace`, `purge_workspace`, `archive_app`, `sync_config`, `deploy_api`, and
`materialize_build` handlers independently. Registration sends exact
control-plane resource IDs;
App registration also sends complete immutable Session runtime settings. Replay is
accepted only when provider state still matches. Workspace registration
completion records only the projection operation result; it does not mutate
the instant-created Workspace or introduce lifecycle state. API modules are
verified before the Cloudflare call. Session runtime
operations download the signed source archive onto the control/build VM, call
its local converter over a restricted Unix socket, publish the prepared image
and manifest to R2, then submit a signed image URL to Fire Fuse with the
external operation ID as their idempotency identity. A Session runtime
operation retries `not_found` while its independent App registration
converges. Transient installation failures retry every 30 seconds for up to 100
attempts. Configuration projection replaces Cloudflare bindings first and the
Fire Fuse cache second; it completes only if the authoritative revision is
still current. Transient provider failures retry every 30 seconds for up to
100 attempts.

The console uses `purge_workspace` for a permanent Workspace delete. The
product transaction archives the Workspace immediately, freeing its active
slug and quota slot, and the worker then waits for registration to settle
before calling Fire Fuse. Fire Fuse terminates active sandboxes, removes the
JuiceFS directory with trash bypassed, and deletes Workspace-scoped runtime
metadata. The archived product Workspace row and completed control operation
remain as the deletion tombstone. The older `archive_workspace` handler stays
available so operations queued before this behavior changed keep their
non-destructive semantics.

Sessions are intentionally not background desired state. The private gateway
asks Fire Fuse's provider-neutral runtime API to create or select a Session and
proxies the request. A newly created request Session follows the completion
barrier and termination path; a targeted existing Session remains reusable and
the request does not take ownership of its lifecycle. The background worker
owns durable product-to-provider convergence; the gateway owns latency-sensitive
runtime routing.

On the control/build VM, set `CANTELOP_COMPUTE_INGRESS_URL` to
`http://10.77.0.1:8084` over WireGuard; do not publish that listener. The worker uses the same
64-hex control token accepted by Fire Fuse. Fire Fuse requires
`CANTELOP_ARTIFACT_DOWNLOAD_ORIGIN` in
`/etc/fire-fuse/artifact-store.env`; it must be the HTTPS origin that appears in
the signed R2 download URL. Commands with another origin are rejected before
any network request, preventing the provider command from becoming a generic
host-side fetch primitive.

Cloudflare and object-store credentials belong only in the web/worker stack.
Fire Fuse receives neither credential. For Build artifacts it receives one
expiring GET URL, then verifies the exact compressed size and SHA-256 digest
before handing the decompressed archive to its existing materializer.

The compute ingress accepts only full App and Workspace registration,
desired App absence, revisioned configuration replacement, and signed-artifact
Build materialization commands. It does not accept customer uploads, identity
resolution, quota administration, or tenant-scoped product reads.

App registration may include an existing content-addressed `build_id`. This is
reserved for authenticated host qualification, where the selected Build is
already present in the trusted host catalog. The service validates the Build
through the same supervisor boundary used by runtime creation; normal product
registration omits the field and continues through signed-artifact Build
materialization.

`CANTELOP_CONFIG_ENCRYPTION_KEY` is a base64url-encoded 32-byte application
key held by the web/worker stack. Secret rows contain only a random nonce and
AES-256-GCM ciphertext authenticated against the App ID and configuration
name. Tenant listings never decrypt or return secret values; only the
projection worker resolves plaintext long enough to replace provider
configuration. Fire Fuse immediately seals projected secrets again with its
root-owned host key. Its revisioned configuration rows are a rebuildable local
runtime cache; PostgreSQL remains the sole product authority.


Managed services that own isolated Apps for their customers can use
[application Fleets](managed-fleets.md), including scoped principals, approved
packages, funded host execution and lifecycle reconciliation.
