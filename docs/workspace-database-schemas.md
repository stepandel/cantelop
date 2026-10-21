# Developer-owned Workspace schemas

Each Workspace has one application-owned SQLite database. Declare managed tables
in `db/schema.ts` with Drizzle using `@cantelop/sdk/schema`. The Session inbox
stays in memory. This workflow installs no mailbox schema or system migrations,
and introduces no privileged mailbox client, table grants, or local authorizer.
API and Session code keep ordinary Workspace SQL access, including DDL.

SDK 1.0 emits `cantelop-runtime.json` (native manifest schema 1) with a
validated, hashed developer snapshot in `database_schema`.
Developers do not generate or commit migration SQL. The CLI applies application
schema changes before opening local Workspace connections and refreshes existing
local databases after schema rebuilds. Hosted activation migrates ready Workspaces
before selecting the new release. Credential issuance initializes new Workspaces
against the active release. Projects without a declared schema keep their existing
runtime-controlled database behavior.

`cantelop_application_migrations` records application SQL, snapshots, digests,
and timestamps transactionally. It is bookkeeping in the same database, not an
access partition. `cantelop database migrations WORKSPACE_ID [--json]` exposes
owner-authorized history and the expected active digest without returning database
credentials or application records.

Additive changes run while the current release stays admitted. Changes to existing
columns or removal of columns/tables require `export const allowDestructiveChanges
= true`; activation closes admission first. A migration failure does not reopen
admission after destructive work. Historical releases do not automatically
down-migrate, including Workspaces already at a newer release's schema. After
destructive changes, older code can be incompatible. Views, custom data migration
hooks, and ambiguous renames are initially unsupported.

## Existing tables and deployment

Only declared objects and the application ledger participate in managed migrations.
Unrelated application tables are left untouched. Retired mailbox tables are neither
installed nor required.
Reserved names keep platform-owned names out of generated
application DDL; they do not restrict ordinary SQL access.

If a table declared in the first managed snapshot already exists without migration
history, initialization fails with `unmanaged_table_conflict` before changing
application data. Automatic baselining is initially unsupported. Use a fresh
Workspace for those managed tables or retain their existing migration mechanism
until an explicit baseline workflow is available. Do not remove populated tables
or fabricate ledger rows to bypass the check.

Use SDK 1.0 and CLI 0.15.2+ to generate and deploy matching Edge/native
manifests. Preserve the application-owned in-memory mailbox runtime; retired DB mailbox adapters and
history readers remain removed.
SDK and platform artifact validation and migration generation must stay aligned.
No fine-grained Turso token qualification is required for this workflow because
credential permissions are unchanged.
