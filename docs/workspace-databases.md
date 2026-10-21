# Managed Workspace databases

Each canonical Workspace owns one remote Turso/libSQL database in Cantelop's
account. The API and all Session sandboxes assigned to that Workspace connect
to the same database. Database access grants access to that entire Workspace's
database; it does not isolate rows between Sessions.

## Provisioning and lifecycle

Workspace creation reserves a `workspace_databases` row in the same PostgreSQL
transaction as the Workspace and its registration operation. SDK 1.0 Workspaces
can register with compute immediately; their application database is provisioned
on the first database access. SDK 1.0 is the only supported SDK, and its
mailbox does not require a remote database. Older eager-provisioning paths in
the implementation are not supported SDK workflows.

First database access verifies App ownership and archival, commits a deterministic
provider name, reconciles ambiguous creation, probes `SELECT 1`, and then returns
credentials. Concurrent requests serialize on the existing database row, including
provisioning and purge. A failed probe exposes no credentials; the next request
retries using the same provider identity. Existing ready databases and fresh host
replay retain their database identity.

Archival blocks credential issuance and retains the database until its purge
runs. Every path that archives a Workspace queues that purge: customer and
operator Workspace deletion, and App archive completion (an archived App never
reopens). Purge first stops compute and removes filesystem state, then commits
the database record as `deleting`, which drops cached credentials, and finally
deletes the provider database and records `deleted`. A provider failure leaves
`deleting` visible and the purge operation retries. Repeated provider deletion
accepts 404; the database record remains as a tombstone. Database provisioning
and purge serialize on the database row. Never delete this record.

A database is never deleted on its own: removing it while its Workspace is live
would strand a Workspace that can never be provisioned again.

## Console surfaces

The customer Workspaces table shows each live Workspace's database as
On first use, Provisioning, Ready, or Provisioning failed (pending with a failed
`register_workspace` operation). It never shows provider names or credentials.

The operator Workspaces table adds the database state, with the Turso database
name on hover, and lifecycle actions: **Delete** archives a live Workspace and
queues its purge; **Retry** re-queues a failed purge; **Purge** queues one for a
Workspace archived without a purge (Apps archived before purges were queued on
App archive). `DELETE /api/admin/workspaces/{id}` backs all three.

**Check database drift** (`GET /api/admin/workspace-databases/drift`) lists the
configured Turso group on demand and compares names under this environment's
`ct-<environment>-` prefix with the control database. It reports *missing*
databases (record `ready`, absent from Turso, for example deleted from the
Turso dashboard; credential requests for that Workspace fail) and *orphaned*
ones (present in Turso with no record or a `deleted` tombstone). The check is
read-only: Cantelop never recreates a missing database, which would hide data
loss, or deletes an orphan automatically. An operator resolves each by hand.

## Credentials and clients

The organization platform token stays in Console/control-worker configuration.
The credential service issues 15-minute, single-database, full-access tokens.
It stores cached tokens encrypted with the existing configuration cipher, with
Workspace identity and a dedicated token name as authenticated context. Tokens
are renewed when fewer than two minutes remain.

API access follows the existing trusted outbound Worker -> origin broker ->
Fire Fuse gateway -> Console path. The gateway supplies the authenticated App
identity; Console verifies that the canonical Workspace belongs to that App.
The generated SDK 1.0 Edge invokes the private
`/__cantelop/v1/workspaces/database/credentials` route with the resolved canonical
`workspace_id`. Workspace operations retain these private v1 paths; Session
operations use integration v2. Public Workspace responses never include credentials.

At guest assignment, Fire Fuse overwrites image defaults with a dedicated
HMAC capability bound to sandbox, App and Workspace. The guest requests
credentials from `POST /internal/v1/runtime/database/credentials` using that
capability as its bearer token, with no Workspace selector. Console verifies
the signature, checks the sandbox's current persisted assignment and lifecycle
through authenticated `GET /sandboxes/{id}/database-access` on the HTTPS
observer, and checks Workspace/App archival. The observer reads only that
Sandbox's current persisted assignment through `GET /v1/sandboxes/{id}` using
its scoped control credential. The response contains only `sandboxId`, `appId`,
`workspaceId`, and `active`; stopped or missing Sandboxes return 404 with
error code `not_found`. A bare 404 means the route is not deployed. Neither
the observer response nor the credential broker's verification is cached.
Credential issuance does not download the dashboard's historical inventory.
A broker running on the private compute network can use provider ingress
instead when no observer origin is configured.
A missing, failed, stopping or terminated sandbox cannot renew credentials.
The capability key never enters the guest.

Credential failures retain the public `database_access_unavailable` response.
Server logs record `runtime_database_credentials_failed` with the failure stage
(`sandbox_verification` or `database_credentials`), an allowlisted reason,
HTTP status when available, and the signed platform identities. Timeouts,
unavailable verification services, invalid responses, and inactive assignments
are distinct reasons. Raw exceptions, URLs, bearer tokens, database credentials,
and provider response bodies are never included.

SDK 1.0 Sessions request database credentials only when their runtime or App
uses the database. Native clients can
use the same endpoint and renew according to `expiresAt`. Database queries go
directly to Turso. The SDK keeps active transactions on their original
connection when it renews credentials, and does not retry SQL operations.
An ambiguous write or commit must be resolved by application-level idempotency.

Already issued database tokens remain usable until their expiry. Turso's
all-token invalidation operation affects every client in the Workspace and
requires a separate operator action; Session shutdown blocks renewal rather
than immediately revoking an individual token.

## Local development

`cantelop dev` supplies disposable SQLite databases through a local libSQL HTTP v2
service. One file per deterministic App/Workspace identity lives under
`.cantelop/dev/<app>/databases/`, separate from persistent Workspace files. API and
Session clients share the same local database in native and Docker modes.
The runner never provisions Turso databases or loads hosted provider credentials.

The SDK must export local database protocol version `1` from its build module.
The local API builder opts into its exact numeric-loopback database origin;
Session launchers inject an explicit development origin, credential endpoint,
and private Session access token. Hosted SDK clients continue requiring Turso
addresses and the production HTTPS broker contract. Local SQL grants are
Workspace-bound, expire after 15 minutes, and exist only for the current runner.
Closing a Session blocks renewal while issued grants remain valid until expiry.
Abandoned HTTP streams roll back after one minute of inactivity; runner shutdown
rolls back and closes all streams. SQL errors do not cause write replay.

Data persists across local rebuilds and runner restarts. To reset it, stop the
runner and remove `.cantelop/dev/<app>/databases/`. The next database access creates
fresh files. Local state is never synchronized with a hosted Workspace database.
See [CLI local development](./cli.md#develop-an-app-locally) for Docker networking
and the rest of the local runtime boundary.

## Deployment configuration

Configure the following server-only values in both Console and the control
worker before deploying the application:

- `CANTELOP_TURSO_ORGANIZATION`: Cantelop-owned organization.
- `CANTELOP_TURSO_GROUP`: pre-created libSQL group in the desired region.
- `CANTELOP_TURSO_ENVIRONMENT`: stable lowercase alphanumeric deployment
  identifier, at most 16 characters. Use `staging` for the current live
  environment. Preview databases must use a separate group and identifier.
- `CANTELOP_TURSO_PLATFORM_TOKEN`: private Platform API credential.
- `CANTELOP_CONFIG_ENCRYPTION_KEY`: existing 32-byte base64url configuration key.

Console on Vercel requires its existing `FIRE_FUSE_OBSERVER_URL` HTTPS origin
and `FIRE_FUSE_STATUS_TOKEN` read credential for live Sandbox verification.
Provider ingress stays on WireGuard for the control worker; it is not reachable
from Vercel. Console also requires `CANTELOP_WORKSPACE_DATABASE_CAPABILITY_KEY`, a distinct
32-byte lowercase hexadecimal key. Fire Fuse receives the same capability key
and `CANTELOP_WORKSPACE_DATABASE_CREDENTIALS_URL`, an HTTPS Console URL ending
in `/internal/v1/runtime/database/credentials`. Do not expose these settings as
`NEXT_PUBLIC_*`, customer configuration, logs, or public build metadata.

Ansible installs the provider values in the builder's private worker environment
and the capability settings in the compute host's root-only environment file.
Use the corresponding `cantelop_control_worker_turso_*` and
`fire_fuse_workspace_database_*` variables; provider token and capability key
come from Vault defaults. No user database setup is required.

## Release qualification

Deploy the Fire Fuse observer control-scope update, observer binary, and nginx
route before activating the Console revision that uses the scoped lookup.
The observer Ansible role qualifies authentication on the new public route.
An older observer fails closed; Console never falls back to the full snapshot.
Qualify a matching live assignment and denial after termination through the
public observer before exercising guest credential renewal.

Use the [deployment runbook](deployment.md) with matching SDK 1.0 artifacts.
Qualify database provisioning, conflict reconciliation, credential renewal,
backend/Session sharing, and purge against a dedicated disposable Workspace.
Confirm file-only Workspace registration does not provision a database, then
verify first database access provisions exactly one provider database.

Verify isolation from another Workspace and denial of credential renewal after
Session termination or Workspace archival. Preserve the environment identifier,
database identity records, and application data across service upgrades. Do not
restore an older SDK or DB-mailbox runtime as a supported rollback target.

The [original SDK 0.13.0 rollout](history/workspace-database-rollout.md) is retained
as historical evidence, including its original maintenance and backfill steps.

## Verification

The feature tests cover provider conflict reconciliation, sanitized failures,
readiness probes, encrypted token caching, authorization, lifecycle ordering,
two-phase purge, purge queueing on App archive, drift classification,
credential renewal, and concurrent provisioning using real PostgreSQL. SDK
checks cover remote clients, direct credentials, transaction retention, and
absence of automatic write replay. Platform releases still require live-provider qualification with Cantelop's
Turso account; local tests alone do not prove provider behavior.
