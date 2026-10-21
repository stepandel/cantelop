import { publicAppHostname } from "../deployment";
import {validateAppEnvironment} from "./declarations";
import {parseProtocolEdgeManifest, type AppDeploymentConfiguration, type EnvironmentDeclaration} from "../api-deployments/protocol-artifact";
import {randomBytes} from "node:crypto";
import { and, asc, eq, ne, notLike } from "drizzle-orm";

import { ControlError } from "../domain/errors";
import { assertResourceID, newResourceID } from "../domain/ids";
import { controlQueryBuilder, executeDrizzle, withTransaction, type SQLPool, type SQLClient, type TransactionClient } from "../db/database";
import { appConfigValues, apps } from "../db/schema";
import { appAccessibleSQL, resolveAppOwnerForUser } from "../db/app-access";
import { ConfigCipher } from "./cipher";
import { validConfigValue, writableConfigName } from "./limits";

export type ConfigValueKind = "variable" | "secret";

export interface EnvironmentUpdate {
  revision: number;
  status: "pending" | "applied" | "failed";
}

export interface ConfigValueView {
  name: string;
  kind: ConfigValueKind;
  value?: string;
  /** Present for secrets, without revealing their value or length. */
  is_empty?: boolean;
  updated_at: string;
}

/** The environment the live release's code declares; values are stored per App. */
export interface EnvironmentDeclarations {
  release_id: string;
  version: number;
  environment: Record<string, EnvironmentDeclaration>;
}

export interface ConfigValueList {
  items: ConfigValueView[];
  /** Null until the App has a live release. */
  declarations: EnvironmentDeclarations | null;
}

export interface ConfigProjectionValue {
  name: string;
  kind: ConfigValueKind;
  value: string;
}

export interface IntegrationCredentials {
  app_id: string;
  slug: string;
  edge_url: string;
  token: string;
  /** While set, the previous token still authenticates on Edge releases that support rotation. */
  previous_token_expires_at: string | null;
  rotation: { revision: number; rotated_at: string; status: "pending" | "applied" | "failed" } | null;
}

const integrationTokenName = "CANTELOP_INTEGRATION_TOKEN";
const previousIntegrationTokenName = "CANTELOP_INTEGRATION_TOKEN_PREVIOUS";
export const defaultIntegrationGraceSeconds = 24 * 60 * 60;
export const maxIntegrationGraceSeconds = 7 * 24 * 60 * 60;

const maxEntries = 128;
const maxScopeBytes = 512 * 1024;

interface PublicConfigRow extends Record<string, unknown> {
  name: string;
  kind: ConfigValueKind;
  value_text: string | null;
  value_bytes: number;
  updated_at: Date;
}

// What a listing shows: never the sealed secret material.
const publicConfigSelection = {
  name: appConfigValues.name,
  kind: appConfigValues.kind,
  value_text: appConfigValues.valueText,
  value_bytes: appConfigValues.valueBytes,
  updated_at: appConfigValues.updatedAt,
};

const configSelection = {
  ...publicConfigSelection,
  secret_nonce: appConfigValues.secretNonce,
  secret_ciphertext: appConfigValues.secretCiphertext,
};

// Platform-managed names are never listed. LIKE treats `_` as a wildcard, so
// it is escaped to match only the literal CANTELOP_ prefix.
const platformConfigNamePattern = "CANTELOP\\_%";

export class ConfigRepository {
  constructor(
    private readonly database: SQLPool,
    private readonly cipher: ConfigCipher,
    private readonly now: () => Date = () => new Date(),
  ) {}

  async listForUser(userID: string, appID: string): Promise<ConfigValueList> {
    const db = controlQueryBuilder(this.database);
    // The reads are independent; nothing is returned unless access resolves.
    const [, result, declarations] = await Promise.all([
      resolveAppOwnerForUser(this.database, userID, appID),
      executeDrizzle(this.database, db.select(publicConfigSelection)
        .from(appConfigValues)
        .where(and(eq(appConfigValues.appId, appID), notLike(appConfigValues.name, platformConfigNamePattern)))
        .orderBy(asc(appConfigValues.name))),
      this.activeDeclarations(appID),
    ]);
    return { items: result.rows.map(publicValue), declarations };
  }

  private async activeDeclarations(appID: string, executor: SQLClient = this.database): Promise<EnvironmentDeclarations | null> {
    const result = await executor.query<{ id: string; version: number; manifest: unknown }>(`SELECT r.id, r.version, artifact.manifest
FROM cantelop_control.apps a
JOIN cantelop_control.releases r ON r.id = a.active_release_id
JOIN cantelop_control.api_artifacts artifact ON artifact.id = r.api_artifact_id
WHERE a.id = $1`, [appID]);
    const row = result.rows[0];
    if (row === undefined) return null;
    return {
      release_id: row.id,
      version: Number(row.version),
      environment: parseProtocolEdgeManifest(row.manifest).environment ?? {},
    };
  }

  async setForUser(userID: string, appID: string, kind: ConfigValueKind, name: string, value: string): Promise<ConfigValueView> {
    return (await this.setValue(userID, appID, kind, name, value)).value;
  }

  /** A null userID is the App itself: it writes only its declared names. */
  private async setValue(userID: string | null, appID: string, kind: ConfigValueKind, name: string, value: string): Promise<{ value: ConfigValueView; revision: number }> {
    validateMutation(userID, appID, kind, name, value);
    return withTransaction(this.database, async (client) => {
      await client.query("SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", [`deployment:${appID}`]);
      const { revision, ownerUserID } = await this.lockMutableApp(client, userID, appID);
      if (userID === null) await this.assertDeclared(client, appID, kind, name, value);
      const existing = await client.query<{ kind: ConfigValueKind; value_bytes: number }>(`SELECT kind, value_bytes
FROM cantelop_control.app_config_values WHERE app_id = $1 AND name = $2`, [appID, name]);
      if (existing.rowCount === 1 && existing.rows[0].kind !== kind) throw new ControlError("conflict");
      const usage = await client.query<{ entries: string; bytes: string }>(`SELECT count(*)::text AS entries,
       coalesce(sum(length(name) + value_bytes), 0)::text AS bytes
FROM cantelop_control.app_config_values WHERE app_id = $1`, [appID]);
      if (usage.rowCount !== 1) throw new Error("configuration usage returned an invalid row count");
      const valueBytes = Buffer.byteLength(value);
      const nextEntries = Number(usage.rows[0].entries) + (existing.rowCount === 0 ? 1 : 0);
      const replacedBytes = existing.rowCount === 1 ? name.length + existing.rows[0].value_bytes : 0;
      const nextBytes = Number(usage.rows[0].bytes) - replacedBytes + name.length + valueBytes;
      if (nextEntries > maxEntries || nextBytes > maxScopeBytes) throw new ControlError("quota_exceeded");
      const encrypted = kind === "secret" ? this.cipher.seal(appID, name, value) : undefined;
      const at = this.now();
      await client.query(`INSERT INTO cantelop_control.app_config_values
  (app_id, name, kind, value_text, secret_nonce, secret_ciphertext, value_bytes, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (app_id, name) DO UPDATE SET
  kind = EXCLUDED.kind, value_text = EXCLUDED.value_text,
  secret_nonce = EXCLUDED.secret_nonce, secret_ciphertext = EXCLUDED.secret_ciphertext,
  value_bytes = EXCLUDED.value_bytes, updated_at = EXCLUDED.updated_at`, [
        appID, name, kind, kind === "variable" ? value : null,
        encrypted?.nonce ?? null, encrypted?.ciphertext ?? null, valueBytes, at,
      ]);
      await this.bumpAndEnqueue(client, ownerUserID, appID, revision, at);
      return { value: { name, kind, ...(kind === "variable" ? { value } : { is_empty: valueBytes === 0 }), updated_at: at.toISOString() }, revision: revision + 1 };
    });
  }

  async deleteForUser(userID: string, appID: string, kind: ConfigValueKind, name: string): Promise<boolean> {
    return (await this.deleteValue(userID, appID, kind, name)).deleted;
  }

  private async deleteValue(userID: string | null, appID: string, kind: ConfigValueKind, name: string): Promise<{ deleted: boolean; revision: number }> {
    validateMutation(userID, appID, kind, name);
    return withTransaction(this.database, async (client) => {
      await client.query("SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", [`deployment:${appID}`]);
      const { revision, ownerUserID } = await this.lockMutableApp(client, userID, appID);
      if (userID === null) await this.assertDeclared(client, appID, kind, name);
      const existing = await client.query<{ kind: ConfigValueKind }>(`SELECT kind FROM cantelop_control.app_config_values
WHERE app_id = $1 AND name = $2`, [appID, name]);
      if (existing.rowCount === 0) return { deleted: false, revision };
      if (existing.rows[0].kind !== kind) throw new ControlError("conflict");
      await client.query(`DELETE FROM cantelop_control.app_config_values WHERE app_id = $1 AND name = $2`, [appID, name]);
      await this.bumpAndEnqueue(client, ownerUserID, appID, revision, this.now());
      return { deleted: true, revision: revision + 1 };
    });
  }

  /** Trusted gateway supplies the App identity; callers cannot select another App or read secrets. */
  async setForApp(appID: string, kind: ConfigValueKind, name: string, value: string): Promise<EnvironmentUpdate> {
    const result = await this.setValue(null, appID, kind, name, value);
    return { revision: result.revision, status: "pending" };
  }

  /** Unsetting an absent value changes nothing, so there is nothing to wait for. */
  async deleteForApp(appID: string, kind: ConfigValueKind, name: string): Promise<EnvironmentUpdate> {
    const result = await this.deleteValue(null, appID, kind, name);
    return { revision: result.revision, status: result.deleted ? "pending" : "applied" };
  }

  async environmentStatus(appID: string, revision: number): Promise<EnvironmentUpdate> {
    assertResourceID("app", appID);
    if (!Number.isSafeInteger(revision) || revision < 0) throw new TypeError("invalid configuration revision");
    // Sessions start on compute, so unlike a token rotation an update is applied only once compute has it too.
    const result = await this.database.query<{ revision: string; applied: boolean; failed: boolean }>(`SELECT a.config_revision::text AS revision,
  ${configSyncAppliedSQL("$2", false)} AS applied,
  COALESCE(${configSyncFailedSQL("$2")}, false) AS failed
FROM cantelop_control.apps a JOIN cantelop_control.users u ON u.id = a.owner_user_id
WHERE a.id = $1 AND a.status <> 'archived' AND u.status = 'active'`, [appID, revision]);
    if (result.rowCount !== 1) throw new ControlError("not_found");
    const row = result.rows[0];
    if (revision > Number(row.revision)) throw new TypeError("invalid configuration revision");
    return { revision, status: revision === 0 || row.applied ? "applied" : row.failed ? "failed" : "pending" };
  }

  /** An undefined value unsets; an empty value is as missing as an unset one for a required declaration. */
  private async assertDeclared(executor: SQLClient, appID: string, kind: ConfigValueKind, name: string, value?: string): Promise<void> {
    const declarations = (await this.activeDeclarations(appID, executor))?.environment;
    const declaration = declarations && Object.hasOwn(declarations, name) ? declarations[name] : undefined;
    if (!declaration || (declaration.secret === true ? "secret" : "variable") !== kind || declaration.required && !(value ?? declaration.default)) throw new TypeError("environment name or kind is not declared, or value is required");
  }

  /** Separate App credentials; never return them through ordinary configuration APIs. */
  async preparePackageDeployment(userID: string, appID: string, packageID: string): Promise<void> {
    await resolveAppOwnerForUser(this.database,userID,appID);
    const result=await this.database.query(`SELECT aa.manifest FROM cantelop_control.fleet_installations i
      JOIN cantelop_control.fleet_packages p ON p.id=i.package_id AND p.fleet_id=i.fleet_id
      JOIN cantelop_control.api_artifacts aa ON aa.id=p.api_artifact_id
      WHERE i.app_id=$1 AND i.package_id=$2 AND i.desired_state='ready'`,[appID,packageID]);
    if(!result.rows[0])throw new TypeError("Unbound package deployment");
    const manifest=parseProtocolEdgeManifest(result.rows[0].manifest);
    validateAppEnvironment(manifest,(await this.projection(appID)).values);
    await this.integrationForUser(userID,appID);
  }

  /** Separate App credentials; never return them through ordinary configuration APIs. */
  async prepareIntegrationDeployment(
    userID: string,
    appID: string,
    configuration: AppDeploymentConfiguration,
  ): Promise<void> {
    await resolveAppOwnerForUser(this.database, userID, appID);
    const app = await this.database.query<{ slug: string }>(
      "SELECT slug FROM cantelop_control.apps WHERE id = $1",
      [appID],
    );
    if (app.rowCount !== 1 || app.rows[0].slug !== configuration.app_name)
      throw new TypeError(
        "App definition name does not match deployment target",
      );
    validateAppEnvironment(
      configuration,
      (await this.projection(appID)).values,
    );
    await this.integrationForUser(userID, appID);
  }

  async integrationForUser(
    userID: string,
    appID: string,
  ): Promise<{ token: string; slug: string }> {
    assertResourceID("user", userID);
    assertResourceID("app", appID);
    return withTransaction(this.database, async (client) => {
      await client.query(
        "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))",
        [`deployment:${appID}`],
      );
      const { revision, ownerUserID } = await this.lockAccessibleApp(
        client,
        userID,
        appID,
      );
      const app = await client.query<{ slug: string }>(
        "SELECT slug FROM cantelop_control.apps WHERE id = $1",
        [appID],
      );
      if (app.rowCount !== 1) throw new ControlError("not_found");
      const name = "CANTELOP_INTEGRATION_TOKEN";
      const stored = await client.query<{
        secret_nonce: Buffer;
        secret_ciphertext: Buffer;
      }>(
        "SELECT secret_nonce, secret_ciphertext FROM cantelop_control.app_config_values WHERE app_id = $1 AND name = $2",
        [appID, name],
      );
      if (stored.rowCount === 1)
        return {
          slug: app.rows[0].slug,
          token: this.cipher.open(
            appID,
            name,
            stored.rows[0].secret_nonce,
            stored.rows[0].secret_ciphertext,
          ),
        };
      const token = randomBytes(32).toString("hex");
      const encrypted = this.cipher.seal(appID, name, token);
      const at = this.now();
      await client.query(
        `INSERT INTO cantelop_control.app_config_values (app_id,name,kind,value_text,secret_nonce,secret_ciphertext,value_bytes,updated_at)
VALUES ($1,$2,'secret',NULL,$3,$4,$5,$6)`,
        [
          appID,
          name,
          encrypted.nonce,
          encrypted.ciphertext,
          Buffer.byteLength(token),
          at,
        ],
      );
      await this.bumpAndEnqueue(client, ownerUserID, appID, revision, at);
      return { slug: app.rows[0].slug, token };
    });
  }

  /** Read-only: a credential exists once the App's first deployment was prepared. */
  async integrationCredentialsForUser(userID: string, appID: string): Promise<IntegrationCredentials> {
    assertResourceID("user", userID);
    assertResourceID("app", appID);
    const result = await this.database.query<IntegrationCredentialRow>(`${integrationCredentialSelection}
WHERE a.id = $1 AND a.status <> 'archived' AND ${appAccessibleSQL()}`, [appID, userID, this.now()]);
    if (result.rowCount !== 1) throw new ControlError("not_found");
    if (result.rows[0].user_status !== "active") throw new ControlError("user_suspended");
    return this.integrationCredentials(appID, result.rows[0]);
  }

  /**
   * Replace the App's integration token. With a grace period the previous token
   * keeps authenticating until it expires; without one it stops as soon as the
   * new configuration reaches the Edge.
   */
  async rotateIntegrationForUser(userID: string, appID: string, graceSeconds: number | "retry"): Promise<IntegrationCredentials> {
    assertResourceID("user", userID);
    assertResourceID("app", appID);
    if (graceSeconds !== "retry" && (!Number.isSafeInteger(graceSeconds) || graceSeconds < 0 || graceSeconds > maxIntegrationGraceSeconds)) {
      throw new TypeError("invalid integration grace period");
    }
    return withTransaction(this.database, async (client) => {
      await client.query("SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", [`deployment:${appID}`]);
      const { revision, ownerUserID } = await this.lockAccessibleApp(client, userID, appID);
      const current = await client.query<{ secret_nonce: Buffer; secret_ciphertext: Buffer }>(
        "SELECT secret_nonce, secret_ciphertext FROM cantelop_control.app_config_values WHERE app_id = $1 AND name = $2",
        [appID, integrationTokenName],
      );
      if (current.rowCount !== 1) throw new ControlError("integration_not_configured");
      const state = await client.query<IntegrationCredentialRow>(`${integrationCredentialSelection}
WHERE a.id = $1`, [appID, userID, this.now()]);
      if (state.rowCount !== 1) throw new ControlError("not_found");
      const credentials = this.integrationCredentials(appID, state.rows[0]);
      if (graceSeconds === "retry") {
        if (credentials.rotation === null) throw new ControlError("conflict");
        // Resend the desired token and retain the original grace period. Never
        // rotate again to recover from a failed or ambiguous provider request.
        const at = this.now();
        await client.query(`UPDATE cantelop_control.app_integration_rotations
SET config_revision = $2 WHERE app_id = $1`, [appID, revision + 1]);
        await this.bumpAndEnqueue(client, ownerUserID, appID, revision, at);
        return { ...credentials, rotation: { ...credentials.rotation, revision: revision + 1,
          status: state.rows[0].active_release_id === null ? "applied" : "pending" } };
      }
      if (credentials.rotation !== null && credentials.rotation.status !== "applied") {
        throw new ControlError("integration_rotation_unresolved");
      }
      if (graceSeconds > 0) {
        // An Edge built before rotation support ignores the previous token, so a
        // grace period would silently cut callers off.
        const live = await client.query<{ manifest: Record<string, unknown> }>(`SELECT artifact.manifest
FROM cantelop_control.apps a
JOIN cantelop_control.releases r ON r.id = a.active_release_id
JOIN cantelop_control.api_artifacts artifact ON artifact.id = r.api_artifact_id
WHERE a.id = $1`, [appID]);
        // Without a live release, the next activation projects the current token.
        const manifest = live.rows[0]?.manifest;
        if (manifest !== undefined && (manifest.kind !== "cantelop-protocol-edge" || manifest.optional_bindings === undefined)) {
          throw new ControlError("integration_rotation_unsupported");
        }
      }
      const previous = this.cipher.open(appID, integrationTokenName, current.rows[0].secret_nonce, current.rows[0].secret_ciphertext);
      const token = randomBytes(32).toString("hex");
      const sealed = this.cipher.seal(appID, integrationTokenName, token);
      const at = this.now();
      await client.query(`UPDATE cantelop_control.app_config_values
SET secret_nonce = $3, secret_ciphertext = $4, value_bytes = $5, updated_at = $6
WHERE app_id = $1 AND name = $2`, [appID, integrationTokenName, sealed.nonce, sealed.ciphertext, Buffer.byteLength(token), at]);
      const kept = graceSeconds > 0 ? this.cipher.seal(appID, previousIntegrationTokenName, previous) : undefined;
      const expiresAt = graceSeconds > 0 ? new Date(at.getTime() + graceSeconds * 1000) : null;
      await client.query(`INSERT INTO cantelop_control.app_integration_rotations
  (app_id, previous_nonce, previous_ciphertext, previous_expires_at, config_revision, rotated_at)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (app_id) DO UPDATE SET
  previous_nonce = EXCLUDED.previous_nonce, previous_ciphertext = EXCLUDED.previous_ciphertext,
  previous_expires_at = EXCLUDED.previous_expires_at, config_revision = EXCLUDED.config_revision,
  rotated_at = EXCLUDED.rotated_at`, [appID, kept?.nonce ?? null, kept?.ciphertext ?? null, expiresAt, revision + 1, at]);
      await this.bumpAndEnqueue(client, ownerUserID, appID, revision, at);
      const app = await client.query<{ slug: string }>("SELECT slug FROM cantelop_control.apps WHERE id = $1", [appID]);
      if (app.rowCount !== 1) throw new ControlError("not_found");
      return {
        app_id: appID,
        slug: app.rows[0].slug,
        edge_url: edgeURL(app.rows[0].slug),
        token,
        previous_token_expires_at: expiresAt?.toISOString() ?? null,
        rotation: { revision: revision + 1, rotated_at: at.toISOString(), status: "pending" },
      };
    });
  }

  private integrationCredentials(appID: string, row: IntegrationCredentialRow): IntegrationCredentials {
    if (row.secret_nonce === null || row.secret_ciphertext === null) throw new ControlError("integration_not_configured");
    const rotation = row.rotation_revision === null || row.rotated_at === null ? null : {
      revision: Number(row.rotation_revision),
      rotated_at: row.rotated_at.toISOString(),
      // Without a live release, the next activation projects the current token.
      status: row.rotation_applied || row.active_release_id === null ? "applied" as const : row.rotation_failed ? "failed" as const : "pending" as const,
    };
    return {
      app_id: appID,
      slug: row.slug,
      edge_url: edgeURL(row.slug),
      token: this.cipher.open(appID, integrationTokenName, row.secret_nonce, row.secret_ciphertext),
      previous_token_expires_at: row.previous_expires_at?.toISOString() ?? null,
      rotation,
    };
  }

  async projection(appID: string): Promise<{ revision: number; values: ConfigProjectionValue[]; systemBindings?:ConfigProjectionValue[] }> {
    assertResourceID("app", appID);
    const db = controlQueryBuilder(this.database);
    const app = await executeDrizzle(this.database, db.select({ config_revision: apps.configRevision })
      .from(apps)
      .where(and(eq(apps.id, appID), ne(apps.status, "archived"))));
    if (app.rowCount !== 1) throw new ControlError("not_found");
    const values = await executeDrizzle(this.database, db.select(configSelection)
      .from(appConfigValues)
      .where(eq(appConfigValues.appId, appID))
      .orderBy(asc(appConfigValues.name)));
    const systemToken = values.rows.find(row => row.name === integrationTokenName);
    const systemBindings: ConfigProjectionValue[] | undefined = systemToken === undefined ? undefined : [
      { name: systemToken.name, kind: "secret", value: this.cipher.open(appID, systemToken.name,
        requiredBuffer(systemToken.secret_nonce), requiredBuffer(systemToken.secret_ciphertext)) },
      { name: "CANTELOP_DEFAULT_KEEP_ALIVE_SECONDS", kind: "variable", value: "900" },
      ...await this.previousIntegrationBindings(appID),
    ];
    return {
      revision: Number(app.rows[0].config_revision),
      ...(systemBindings === undefined ? {} : { systemBindings }),
      values: values.rows.filter(row => !row.name.startsWith("CANTELOP_")).map(row => ({
        name: row.name,
        kind: row.kind,
        value: row.kind === "variable" ? requiredText(row) : this.cipher.open(appID, row.name,
          requiredBuffer(row.secret_nonce), requiredBuffer(row.secret_ciphertext)),
      })),
    };
  }

  /** The Edge enforces the expiry itself; an expired token is simply no longer projected. */
  private async previousIntegrationBindings(appID: string): Promise<ConfigProjectionValue[]> {
    const result = await this.database.query<{ previous_nonce: Buffer; previous_ciphertext: Buffer; previous_expires_at: Date }>(
      `SELECT previous_nonce, previous_ciphertext, previous_expires_at FROM cantelop_control.app_integration_rotations
WHERE app_id = $1 AND previous_expires_at > $2`, [appID, this.now()]);
    if (result.rowCount !== 1) return [];
    const row = result.rows[0];
    return [
      { name: previousIntegrationTokenName, kind: "secret", value: this.cipher.open(appID, previousIntegrationTokenName, row.previous_nonce, row.previous_ciphertext) },
      { name: "CANTELOP_INTEGRATION_TOKEN_PREVIOUS_EXPIRES_AT", kind: "variable", value: row.previous_expires_at.toISOString() },
    ];
  }

  async revision(appID: string): Promise<number> {
    assertResourceID("app", appID);
    const db = controlQueryBuilder(this.database);
    const result = await executeDrizzle(this.database, db.select({ config_revision: apps.configRevision })
      .from(apps)
      .where(and(eq(apps.id, appID), ne(apps.status, "archived"))));
    if (result.rowCount !== 1) throw new ControlError("not_found");
    return Number(result.rows[0].config_revision);
  }

  /** The App itself acts for its active owner; a user must be able to access the App. */
  private async lockMutableApp(client: TransactionClient, userID: string | null, appID: string): Promise<{ revision: number; ownerUserID: string }> {
    if (userID !== null) return this.lockAccessibleApp(client, userID, appID);
    const result = await client.query<{ config_revision: string; owner_user_id: string }>(`SELECT a.config_revision::text, a.owner_user_id
FROM cantelop_control.apps a JOIN cantelop_control.users u ON u.id = a.owner_user_id
WHERE a.id = $1 AND a.status <> 'archived' AND u.status = 'active'
FOR UPDATE OF a, u`, [appID]);
    if (result.rowCount !== 1) throw new ControlError("not_found");
    return { revision: Number(result.rows[0].config_revision), ownerUserID: result.rows[0].owner_user_id };
  }

  private async lockAccessibleApp(client: TransactionClient, userID: string, appID: string): Promise<{ revision: number; ownerUserID: string }> {
    const result = await client.query<{ config_revision: string; owner_user_id: string; user_status: "active" | "suspended" }>(`SELECT a.config_revision::text, a.owner_user_id, actor.status AS user_status
FROM cantelop_control.apps a JOIN cantelop_control.users actor ON actor.id = $2
WHERE a.id = $1 AND a.status <> 'archived'
  AND ${appAccessibleSQL()}
FOR UPDATE OF a, actor`, [appID, userID]);
    if (result.rowCount !== 1) throw new ControlError("not_found");
    if (result.rows[0].user_status !== "active") throw new ControlError("user_suspended");
    return { revision: Number(result.rows[0].config_revision), ownerUserID: result.rows[0].owner_user_id };
  }

  private async bumpAndEnqueue(client: TransactionClient, userID: string, appID: string, revision: number, at: Date): Promise<void> {
    const next = revision + 1;
    const updated = await client.query(`UPDATE cantelop_control.apps
SET config_revision = $2, updated_at = $3 WHERE id = $1 AND config_revision = $4`, [appID, next, at, revision]);
    if (updated.rowCount !== 1) throw new ControlError("conflict");
    await client.query(`UPDATE cantelop_control.operations
SET status = 'failed', error_code = 'config_superseded', updated_at = $2
WHERE app_id = $1 AND kind = 'sync_config' AND status = 'queued' AND locked_by IS NULL`, [appID, at]);
    await client.query(`INSERT INTO cantelop_control.operations
  (id, owner_user_id, app_id, kind, status, idempotency_key, input, available_at, created_at, updated_at)
VALUES ($1, $2, $3, 'sync_config', 'queued', $4, $5::jsonb, $6, $6, $6)`, [
      newResourceID("operation"), userID, appID, `sync-config:${appID}:${next}`,
      JSON.stringify({ revision: next }), at,
    ]);
  }
}

function validateMutation(userID: string | null, appID: string, kind: ConfigValueKind, name: string, value?: string): void {
  if (userID !== null) assertResourceID("user", userID);
  assertResourceID("app", appID);
  if ((kind !== "variable" && kind !== "secret") || !writableConfigName(name) || (value !== undefined && !validConfigValue(value))) {
    throw new TypeError("invalid configuration value");
  }
}

function publicValue(row: PublicConfigRow): ConfigValueView {
  return {
    name: row.name,
    kind: row.kind,
    ...(row.kind === "variable" ? { value: requiredText(row) } : { is_empty: row.value_bytes === 0 }),
    updated_at: row.updated_at.toISOString(),
  };
}

function requiredText(row: PublicConfigRow): string {
  if (row.value_text === null) throw new Error("invalid variable configuration record");
  return row.value_text;
}

function requiredBuffer(value: Buffer | null): Buffer {
  if (!value) throw new Error("invalid secret configuration record");
  return value;
}

interface IntegrationCredentialRow extends Record<string, unknown> {
  slug: string;
  active_release_id: string | null;
  user_status: "active" | "suspended";
  secret_nonce: Buffer | null;
  secret_ciphertext: Buffer | null;
  previous_expires_at: Date | null;
  rotation_revision: string | null;
  rotated_at: Date | null;
  rotation_applied: boolean | null;
  rotation_failed: boolean | null;
}

// Whether a sync at or after the revision reached Edge (and compute, unless
// edgeSuffices). A newer sync supersedes older ones, so only the newest decides failure.
function configSyncAppliedSQL(revision: string, edgeSuffices: boolean): string {
  return `EXISTS (SELECT 1 FROM cantelop_control.operations o
    WHERE o.app_id = a.id AND o.kind = 'sync_config'
      AND (${edgeSuffices ? `(o.progress = 'edge_config_applied' AND (o.input->>'revision')::bigint >= ${revision}) OR ` : ""}(o.status = 'succeeded' AND (o.output->>'revision')::bigint >= ${revision})))`;
}

function configSyncFailedSQL(revision: string): string {
  return `(SELECT o.status = 'failed' AND o.error_code <> 'config_superseded' FROM cantelop_control.operations o
    WHERE o.app_id = a.id AND o.kind = 'sync_config' AND (o.input->>'revision')::bigint >= ${revision}
    ORDER BY (o.input->>'revision')::bigint DESC, o.created_at DESC LIMIT 1)`;
}

// A rotation is live once Edge accepted a configuration at or after its
// revision, even if the subsequent compute sync failed.
const integrationCredentialSelection = `SELECT a.slug, a.active_release_id, actor.status AS user_status,
  v.secret_nonce, v.secret_ciphertext,
  CASE WHEN r.previous_expires_at > $3 THEN r.previous_expires_at END AS previous_expires_at,
  r.config_revision::text AS rotation_revision, r.rotated_at,
  ${configSyncAppliedSQL("r.config_revision", true)} AS rotation_applied,
  ${configSyncFailedSQL("r.config_revision")} AS rotation_failed
FROM cantelop_control.apps a
JOIN cantelop_control.users actor ON actor.id = $2
LEFT JOIN cantelop_control.app_config_values v ON v.app_id = a.id AND v.name = '${integrationTokenName}'
LEFT JOIN cantelop_control.app_integration_rotations r ON r.app_id = a.id`;

function edgeURL(slug: string): string {
  return `https://${publicAppHostname(slug)}`;
}
