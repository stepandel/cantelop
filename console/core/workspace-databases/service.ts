import { createClient } from "@libsql/client/web";
import { synchronizeApplicationSchema, validateApplicationSchema, type ApplicationDatabaseSchema } from "./schema";
import { withTransaction, type SQLPool, type TransactionClient } from "../db/database";
import { ConfigCipher } from "../configuration/cipher";
import { databaseName, type WorkspaceDatabaseProvider } from "./provider";

export interface DatabaseCredentials { url: string; authToken: string; expiresAt: string }
export class DatabaseAccessError extends Error {
  constructor(readonly code: "database_not_ready" | "workspace_not_found") { super(code); }
}
interface DatabaseRow extends Record<string, unknown> {
  managed_policy?:Record<string,unknown>|null; workspace_id: string; app_id: string; archived_at: Date | null; app_status: string;
  provider_name: string | null; provider_id: string | null; url: string | null;
  status: "pending" | "ready" | "deleting" | "deleted";
  token_nonce: Buffer | null; token_ciphertext: Buffer | null; token_expires_at: Date | null;
}

export class WorkspaceDatabaseService {
  constructor(private readonly pool: SQLPool, private readonly provider: WorkspaceDatabaseProvider,
    private readonly environment: string, private readonly cipher: ConfigCipher,
    private readonly now: () => Date = () => new Date(),
    private readonly initializeDatabase?: (credentials: DatabaseCredentials) => Promise<void>,
    private readonly clientFactory: typeof createClient = createClient) {
    databaseName(environment, "wsp_00000000000000000000000000000000");
  }

  async ensure(workspaceID: string): Promise<void> {
    // Commit the name before calling Turso: it must survive ambiguous responses
    // and remain stable even if deployment configuration subsequently changes.
    await this.locked(workspaceID, async (client, row) => {
      this.active(row);
      if (row.status === "deleting" || row.status === "deleted") throw new DatabaseAccessError("workspace_not_found");
      if (!row.provider_name) await client.query(`UPDATE cantelop_control.workspace_databases
SET provider_name = $2, updated_at = now() WHERE workspace_id = $1`, [workspaceID, databaseName(this.environment, workspaceID)]);
    });
    await this.locked(workspaceID, async (client, row) => {
      this.active(row);
      if (row.status === "ready") return;
      if (row.status !== "pending" || !row.provider_name) throw new DatabaseAccessError("workspace_not_found");
      const database = await this.provider.ensure(row.provider_name,row.managed_policy?{sizeLimit:256*1024**2}:undefined);
      row.url = database.url;
      const credentials = await this.mint(client, row);
      await this.provider.probe(credentials.url, credentials.authToken);
      await this.initializeDatabase?.(credentials);
      await client.query(`UPDATE cantelop_control.workspace_databases
SET provider_id = $2, url = $3, status = 'ready', updated_at = now() WHERE workspace_id = $1`, [workspaceID, database.id, database.url]);
    });
  }

  async credentials(appID: string, workspaceID: string): Promise<DatabaseCredentials> {
    const credentials = await this.resolveCredentials(appID, workspaceID);
    await this.initializeDatabase?.(credentials);
    return credentials;
  }

  private async resolveCredentials(appID: string, workspaceID: string, initialize = true): Promise<DatabaseCredentials> {
    if (!/^wsp_[0-9a-f]{32}$/.test(workspaceID)) throw new DatabaseAccessError("workspace_not_found");
    const result = await this.pool.query<DatabaseRow>(`SELECT d.*, w.app_id, w.archived_at, a.status AS app_status, fp.policy AS managed_policy
FROM cantelop_control.workspace_databases d JOIN cantelop_control.workspaces w ON w.id = d.workspace_id
JOIN cantelop_control.apps a ON a.id = w.app_id LEFT JOIN cantelop_control.fleet_policies fp ON fp.app_id=a.id WHERE d.workspace_id = $1`, [workspaceID]);
    const row = result.rows[0];
    if (!row) throw new DatabaseAccessError("workspace_not_found");
    this.active(row);
    if (row.app_id !== appID) throw new DatabaseAccessError("workspace_not_found");
    if (row.status === "pending" && initialize) {
      // Ownership and archival were checked above; provisioning remains serialized with purge.
      await this.ensure(workspaceID);
      return this.resolveCredentials(appID, workspaceID, false);
    }
    if (row.status !== "ready" || !row.url) throw new DatabaseAccessError("database_not_ready");
    const cached = this.cachedCredentials(row);
    if (cached) return cached;
    // Serialize token renewal and recheck state after taking the lock.
    return this.locked(workspaceID, async (client, row) => {
      this.active(row);
      if (row.app_id !== appID) throw new DatabaseAccessError("workspace_not_found");
      if (row.status !== "ready" || !row.url) throw new DatabaseAccessError("database_not_ready");
      const cached = this.cachedCredentials(row);
      if (cached) return cached;
      return this.mint(client, row);
    });
  }

  /** Initialize the active developer schema before returning ordinary Workspace credentials. */
  async applicationCredentials(appID: string, workspaceID: string): Promise<DatabaseCredentials> {
    const credentials = await this.credentials(appID, workspaceID);
    const schema = await this.releaseSchema(appID);
    if (schema) await this.applyApplicationSchema(credentials, schema, await this.newerSchemaDigests(appID));
    return credentials;
  }

  /** Additive changes run before activation; new Workspaces use the active manifest. */
  async migrateRelease(appID: string, releaseID: string, closeAdmission?: () => Promise<void>): Promise<void> {
    const schema = await this.releaseSchema(appID, releaseID);
    if (!schema) return;
    const newer = await this.newerSchemaDigests(appID, releaseID);
    if (schema?.allowDestructiveChanges) {
      if (!closeAdmission) throw new Error("destructive_migration_requires_closed_admission");
      await closeAdmission();
    }
    const workspaces = await this.pool.query<{ workspace_id: string }>(`SELECT d.workspace_id FROM cantelop_control.workspace_databases d
JOIN cantelop_control.workspaces w ON w.id=d.workspace_id WHERE w.app_id=$1 AND w.archived_at IS NULL AND d.status='ready'`, [appID]);
    for (const row of workspaces.rows) {
      const credentials = await this.credentials(appID, row.workspace_id);
      await this.applyApplicationSchema(credentials, schema, newer);
    }
  }

  async migrationStatus(appID: string, workspaceID: string) {
    const credentials = await this.resolveCredentials(appID, workspaceID);
    const db = this.clientFactory({ url: credentials.url, authToken: credentials.authToken });
    try {
      const tables = (await db.execute("SELECT name FROM sqlite_schema WHERE type='table'")).rows.map(row => String(row.name));
      const application = tables.includes("cantelop_application_migrations") ? (await db.execute("SELECT digest, statements, applied_at FROM cantelop_application_migrations ORDER BY sequence DESC LIMIT 1000")).rows.map(row => ({ digest: String(row.digest), statements: JSON.parse(String(row.statements)) as string[], applied_at: Number(row.applied_at) })) : [];
      const expected = await this.releaseSchema(appID);
      const appliedDigest = application[0]?.digest;
      const retainedNewerSchema = expected !== undefined && appliedDigest !== undefined && appliedDigest !== expected.digest &&
        (await this.newerSchemaDigests(appID)).has(appliedDigest);
      return { workspace_id: workspaceID, application, expected_application_digest: expected?.digest ?? null,
        retained_newer_schema: retainedNewerSchema };
    } finally { db.close(); }
  }

  private async releaseSchema(appID: string, releaseID?: string): Promise<ApplicationDatabaseSchema | undefined> {
    const result = await this.pool.query<{ manifest: { database_schema?: unknown } }>(`SELECT coalesce(runtime.manifest,artifact.manifest) AS manifest FROM cantelop_control.releases r
JOIN cantelop_control.api_artifacts artifact ON artifact.id=r.api_artifact_id
JOIN cantelop_control.session_runtime_artifacts runtime ON runtime.id=r.session_runtime_artifact_id
WHERE r.app_id=$1 AND ${releaseID ? "r.id=$2" : "r.status='active'"}`, releaseID ? [appID, releaseID] : [appID]);
    const schema = result.rows[0]?.manifest.database_schema;
    return schema === undefined ? undefined : validateApplicationSchema(schema);
  }

  /** Schema digests of releases newer than the given (or active) release: rollbacks never down-migrate. */
  private async newerSchemaDigests(appID: string, releaseID?: string): Promise<Set<string>> {
    const result = await this.pool.query<{ manifest: { database_schema?: { digest?: unknown } } }>(`SELECT coalesce(runtime.manifest,artifact.manifest) AS manifest FROM cantelop_control.releases r
JOIN cantelop_control.api_artifacts artifact ON artifact.id=r.api_artifact_id
JOIN cantelop_control.session_runtime_artifacts runtime ON runtime.id=r.session_runtime_artifact_id
WHERE r.app_id=$1 AND r.version > (SELECT version FROM cantelop_control.releases WHERE app_id=$1 AND ${releaseID ? "id=$2" : "status='active'"})`, releaseID ? [appID, releaseID] : [appID]);
    return new Set(result.rows.map(row => row.manifest.database_schema?.digest).filter((digest): digest is string => typeof digest === "string"));
  }

  private async applyApplicationSchema(credentials: DatabaseCredentials, schema: ApplicationDatabaseSchema, newerDigests: ReadonlySet<string>): Promise<void> {
    const db = this.clientFactory({ url: credentials.url, authToken: credentials.authToken });
    try { await synchronizeApplicationSchema(db, schema, newerDigests); }
    finally { db.close(); }
  }

  private cachedCredentials(row: DatabaseRow): DatabaseCredentials | undefined {
    if (row.url && row.token_expires_at && row.token_expires_at.getTime() > this.now().getTime() + 120_000 && row.token_nonce && row.token_ciphertext) {
      return { url: row.url, authToken: this.cipher.open(row.workspace_id, "workspace-database-token-v1", row.token_nonce, row.token_ciphertext), expiresAt: row.token_expires_at.toISOString() };
    }
  }

  async purge(workspaceID: string): Promise<void> {
    // Commit 'deleting' first so operators can see a purge waiting on the
    // provider; it also blocks provisioning and drops cached credentials.
    await this.locked(workspaceID, async (client, row) => {
      if (row.archived_at === null && row.app_status !== "archived") throw new DatabaseAccessError("workspace_not_found");
      if (row.status === "deleting" || row.status === "deleted") return;
      await client.query(`UPDATE cantelop_control.workspace_databases
SET status = 'deleting', token_nonce = NULL, token_ciphertext = NULL, token_expires_at = NULL, updated_at = now()
WHERE workspace_id = $1`, [workspaceID]);
    });
    await this.locked(workspaceID, async (client, row) => {
      if (row.status === "deleted") return;
      if (row.provider_name) await this.provider.delete(row.provider_name);
      await client.query(`UPDATE cantelop_control.workspace_databases
SET status = 'deleted', token_nonce = NULL, token_ciphertext = NULL, token_expires_at = NULL, updated_at = now()
WHERE workspace_id = $1`, [workspaceID]);
    });
  }

  private active(row: DatabaseRow): void {
    if (row.archived_at !== null || row.app_status === "archived") throw new DatabaseAccessError("workspace_not_found");
  }
  private async mint(client: TransactionClient, row: DatabaseRow): Promise<DatabaseCredentials> {
    if (!row.provider_name || !row.url) throw new DatabaseAccessError("database_not_ready");
    const authToken = await this.provider.token(row.provider_name);
    const expiresAt = tokenExpiration(authToken, this.now());
    const sealed = this.cipher.seal(row.workspace_id, "workspace-database-token-v1", authToken);
    await client.query(`UPDATE cantelop_control.workspace_databases
SET token_nonce = $2, token_ciphertext = $3, token_expires_at = $4, updated_at = now() WHERE workspace_id = $1`,
    [row.workspace_id, sealed.nonce, sealed.ciphertext, expiresAt]);
    return { url: row.url, authToken, expiresAt: expiresAt.toISOString() };
  }
  private async locked<T>(workspaceID: string, action: (client: TransactionClient, row: DatabaseRow) => Promise<T>): Promise<T> {
    if (!/^wsp_[0-9a-f]{32}$/.test(workspaceID)) throw new DatabaseAccessError("workspace_not_found");
    return withTransaction(this.pool, async (client) => {
      const result = await client.query<DatabaseRow>(`SELECT d.*, w.app_id, w.archived_at, a.status AS app_status, fp.policy AS managed_policy
FROM cantelop_control.workspace_databases d JOIN cantelop_control.workspaces w ON w.id = d.workspace_id
JOIN cantelop_control.apps a ON a.id = w.app_id LEFT JOIN cantelop_control.fleet_policies fp ON fp.app_id=a.id WHERE d.workspace_id = $1 FOR UPDATE OF d`, [workspaceID]);
      if (!result.rows[0]) throw new DatabaseAccessError("workspace_not_found");
      return action(client, result.rows[0]);
    }, { lockTimeoutMilliseconds: 5_000 });
  }
}
export function tokenExpiration(token: string, now: Date): Date {
  try {
    const claims = JSON.parse(Buffer.from(token.split(".")[1]!, "base64url").toString());
    const expires = Number(claims.exp) * 1000;
    if (!Number.isSafeInteger(expires) || expires <= now.getTime() + 120_000 || expires > now.getTime() + 960_000) throw new Error();
    return new Date(expires);
  } catch { throw new Error("invalid database token expiration"); }
}
