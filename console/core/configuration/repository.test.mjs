import { expect, test } from "bun:test";

import { ConfigCipher } from "./cipher";
import { ConfigRepository } from "./repository";

const userID = "usr_0123456789abcdef0123456789abcdef";
const appID = "app_0123456789abcdef0123456789abcdef";

function fakePool(handler) {
  const statements = [];
  const client = {
    async query(sql, values = []) {
      statements.push({ sql, values });
      if (["BEGIN", "COMMIT", "ROLLBACK"].includes(sql))
        return { rowCount: null, rows: [] };
      return handler(sql, values);
    },
    release() {},
  };
  return {
    statements,
    async connect() {
      return client;
    },
    query: client.query,
  };
}

test.each(["never-store-plaintext", ""])("stores encrypted secrets and reports emptiness (%j)", async (value) => {
  const at = new Date("2026-08-12T12:00:00Z");
  const pool = fakePool((sql) => {
    if (sql.startsWith("SELECT pg_advisory_xact_lock"))
      return { rowCount: 1, rows: [{}] };
    if (sql.startsWith("SELECT a.config_revision"))
      return {
        rowCount: 1,
        rows: [
          {
            config_revision: "0",
            owner_user_id: userID,
            user_status: "active",
          },
        ],
      };
    if (sql.startsWith("SELECT kind, value_bytes"))
      return { rowCount: 0, rows: [] };
    if (sql.startsWith("SELECT count"))
      return { rowCount: 1, rows: [{ entries: "0", bytes: "0" }] };
    if (sql.startsWith("UPDATE cantelop_control.apps"))
      return { rowCount: 1, rows: [] };
    if (
      sql.startsWith("INSERT INTO") ||
      sql.startsWith("UPDATE cantelop_control.operations")
    )
      return { rowCount: 1, rows: [] };
    throw new Error(`unexpected query ${sql}`);
  });
  const repository = new ConfigRepository(
    pool,
    new ConfigCipher(Buffer.alloc(32, 9)),
    () => at,
  );
  expect(
    await repository.setForUser(
      userID,
      appID,
      "secret",
      "API_TOKEN",
      value,
    ),
  ).toEqual({
    name: "API_TOKEN",
    kind: "secret",
    is_empty: value === "",
    updated_at: at.toISOString(),
  });
  const stored = pool.statements.find(({ sql }) =>
    sql.startsWith("INSERT INTO cantelop_control.app_config_values"),
  );
  expect(stored.values[3]).toBeNull();
  expect(stored.values[4]).toBeInstanceOf(Buffer);
  expect(stored.values[5].includes(Buffer.from("never-store-plaintext"))).toBe(
    false,
  );
  const operation = pool.statements.find(({ sql }) =>
    sql.startsWith("INSERT INTO cantelop_control.operations"),
  );
  expect(operation.values[1]).toBe(userID);
  expect(operation.values[3]).toBe(`sync-config:${appID}:1`);
  expect(JSON.parse(operation.values[4])).toEqual({ revision: 1 });
  const superseded = pool.statements.find(({ sql }) =>
    sql.startsWith("UPDATE cantelop_control.operations"),
  );
  expect(superseded.sql).toContain("updated_at = $2");
  expect(superseded.values).toEqual([appID, at]);
  expect(pool.statements.at(-1).sql).toBe("COMMIT");
});

const releaseID = "rel_0123456789abcdef0123456789abcdef";

function declaringEdgeManifest(environment) {
  return {
    schema_version: 1,
    kind: "cantelop-protocol-edge",
    main_module: "worker.mjs",
    app_name: "agent",
    session_runtime_id: "runtime-v1",
    cli_build_protocol_version: 6,
    integration_protocol_version: 2,
    required_bindings: ["CANTELOP_INTEGRATION_TOKEN"],
    default_keep_alive_binding: "CANTELOP_DEFAULT_KEEP_ALIVE_SECONDS",
    webhooks: [],
    homepage: false,
    ...(environment === undefined ? {} : { environment }),
  };
}

function listingPool(activeManifest) {
  return fakePool((sql) => {
    if (sql.startsWith("SELECT a.owner_user_id"))
      return { rowCount: 1, rows: [{ owner_user_id: userID, user_status: "active" }] };
    if (sql.startsWith("select") && sql.includes('from "cantelop_control"."app_config_values"'))
      return { rowCount: 0, rows: [] };
    if (sql.startsWith("SELECT r.id, r.version, artifact.manifest"))
      return activeManifest === undefined
        ? { rowCount: 0, rows: [] }
        : { rowCount: 1, rows: [{ id: releaseID, version: 3, manifest: activeManifest }] };
    throw new Error(`unexpected query ${sql}`);
  });
}

test("lists the live release's environment declarations", async () => {
  const repository = new ConfigRepository(
    listingPool(declaringEdgeManifest({
      OPENAI_API_KEY: { secret: true, required: true },
      MODEL: { default: "sonnet" },
    })),
    new ConfigCipher(Buffer.alloc(32, 9)),
  );
  expect(await repository.listForUser(userID, appID)).toEqual({
    items: [],
    declarations: {
      release_id: releaseID,
      version: 3,
      environment: {
        OPENAI_API_KEY: { secret: true, required: true },
        MODEL: { default: "sonnet" },
      },
    },
  });
});

test("a release without an environment declares nothing", async () => {
  const repository = new ConfigRepository(
    listingPool(declaringEdgeManifest(undefined)),
    new ConfigCipher(Buffer.alloc(32, 9)),
  );
  expect((await repository.listForUser(userID, appID)).declarations).toEqual({
    release_id: releaseID,
    version: 3,
    environment: {},
  });
});

test("Apps without a live release have no declarations yet", async () => {
  const repository = new ConfigRepository(listingPool(undefined), new ConfigCipher(Buffer.alloc(32, 9)));
  expect((await repository.listForUser(userID, appID)).declarations).toBeNull();
});

test.each([0, 6])("lists variables while reporting secret emptiness (%j bytes)", async (secretBytes) => {
  const at = new Date("2026-08-12T12:00:00Z");
  const pool = fakePool((sql, values) => {
    if (sql.startsWith("SELECT r.id, r.version, artifact.manifest"))
      return { rowCount: 0, rows: [] };
    if (sql.startsWith("SELECT a.owner_user_id")) {
      return {
        rowCount: 1,
        rows: [{ owner_user_id: userID, user_status: "active" }],
      };
    }
    if (
      sql.startsWith("select") &&
      sql.includes('from "cantelop_control"."apps"')
    ) {
      return { rowCount: 1, rows: [{ id: appID }] };
    }
    if (
      sql.startsWith("select") &&
      sql.includes('from "cantelop_control"."app_config_values"')
    ) {
      // Listings never read sealed secrets, and platform names stay in SQL.
      expect(sql).not.toContain("secret_nonce");
      expect(sql).not.toContain("secret_ciphertext");
      expect(sql).toContain('"name" not like $2');
      expect(values).toEqual([appID, "CANTELOP\\_%"]);
    }
    if (
      sql.startsWith("select") &&
      sql.includes('from "cantelop_control"."app_config_values"')
    )
      return {
        rowCount: 2,
        rows: [
          {
            name: "API_TOKEN",
            kind: "secret",
            value_text: null,
            secret_nonce: Buffer.alloc(12),
            secret_ciphertext: Buffer.alloc(16),
            value_bytes: secretBytes,
            updated_at: at,
          },
          {
            name: "LOG_LEVEL",
            kind: "variable",
            value_text: "debug",
            secret_nonce: null,
            secret_ciphertext: null,
            value_bytes: 5,
            updated_at: at,
          },
        ],
      };
    throw new Error(`unexpected query ${sql}`);
  });
  const repository = new ConfigRepository(
    pool,
    new ConfigCipher(Buffer.alloc(32, 9)),
  );
  expect(await repository.listForUser(userID, appID)).toEqual({
    items: [
      { name: "API_TOKEN", kind: "secret", is_empty: secretBytes === 0, updated_at: at.toISOString() },
      {
        name: "LOG_LEVEL",
        kind: "variable",
        value: "debug",
        updated_at: at.toISOString(),
      },
    ],
    declarations: null,
  });
});

test("integration credentials are encrypted, stable and separate from user configuration", async () => {
  const cipher = new ConfigCipher(Buffer.alloc(32, 9));
  let encrypted;
  let inserts = 0;
  const pool = fakePool((sql, values = []) => {
    if (
      sql === "BEGIN" ||
      sql === "COMMIT" ||
      sql === "ROLLBACK" ||
      sql.startsWith("SELECT pg_advisory") ||
      sql.startsWith("UPDATE") ||
      sql.startsWith("INSERT INTO cantelop_control.operations")
    )
      return { rowCount: 1, rows: [] };
    if (sql.startsWith("SELECT a.config_revision"))
      return {
        rowCount: 1,
        rows: [
          {
            config_revision: "0",
            owner_user_id: userID,
            user_status: "active",
          },
        ],
      };
    if (sql.startsWith("SELECT slug"))
      return { rowCount: 1, rows: [{ slug: "agent" }] };
    if (sql.startsWith("SELECT secret_nonce"))
      return encrypted
        ? { rowCount: 1, rows: [encrypted] }
        : { rowCount: 0, rows: [] };
    if (sql.startsWith("INSERT INTO cantelop_control.app_config_values")) {
      inserts++;
      encrypted = { secret_nonce: values[2], secret_ciphertext: values[3] };
      expect(values.includes("CANTELOP_INTEGRATION_TOKEN")).toBe(true);
      return { rowCount: 1, rows: [] };
    }
    throw new Error("unexpected query");
  });
  const repo = new ConfigRepository(pool, cipher);
  const first = await repo.integrationForUser(userID, appID);
  const second = await repo.integrationForUser(userID, appID);
  expect(first).toEqual(second);
  expect(first.token).toMatch(/^[0-9a-f]{64}$/);
  expect(inserts).toBe(1);
  expect(encrypted.secret_ciphertext.includes(Buffer.from(first.token))).toBe(
    false,
  );
});

test("integration credentials project only to system Edge bindings", async () => {
  const cipher = new ConfigCipher(Buffer.alloc(32, 9));
  const token = cipher.seal(
    appID,
    "CANTELOP_INTEGRATION_TOKEN",
    "private-integration-token",
  );
  const pool = fakePool((sql) => {
    if (
      sql.startsWith("select") &&
      sql.includes('from "cantelop_control"."apps"')
    )
      return { rowCount: 1, rows: [{ config_revision: "2" }] };
    if (
      sql.startsWith("select") &&
      sql.includes('from "cantelop_control"."app_config_values"')
    )
      return {
        rowCount: 2,
        rows: [
          {
            name: "CANTELOP_INTEGRATION_TOKEN",
            kind: "secret",
            secret_nonce: token.nonce,
            secret_ciphertext: token.ciphertext,
          },
          { name: "LOG_LEVEL", kind: "variable", value_text: "debug" },
        ],
      };
    if (sql.includes("app_integration_rotations")) return { rowCount: 0, rows: [] };
    throw new Error(`unexpected query ${sql}`);
  });
  const projection = await new ConfigRepository(pool, cipher).projection(appID);
  expect(projection.values).toEqual([
    { name: "LOG_LEVEL", kind: "variable", value: "debug" },
  ]);
  expect(projection.systemBindings).toEqual([
    {
      name: "CANTELOP_INTEGRATION_TOKEN",
      kind: "secret",
      value: "private-integration-token",
    },
    {
      name: "CANTELOP_DEFAULT_KEEP_ALIVE_SECONDS",
      kind: "variable",
      value: "900",
    },
  ]);
});

const integrationName = "CANTELOP_INTEGRATION_TOKEN";
const previousName = "CANTELOP_INTEGRATION_TOKEN_PREVIOUS";

function rotationPool({ cipher, token = "a".repeat(64), manifest, revision = "4", rotation = null, applied = false } = {}) {
  const sealed = token === null ? undefined : cipher.seal(appID, integrationName, token);
  const writes = {};
  const pool = fakePool((sql, values = []) => {
    if (sql.startsWith("SELECT pg_advisory")) return { rowCount: 1, rows: [{}] };
    if (sql.startsWith("SELECT a.config_revision"))
      return { rowCount: 1, rows: [{ config_revision: revision, owner_user_id: userID, user_status: "active" }] };
    if (sql.startsWith("SELECT secret_nonce"))
      return sealed ? { rowCount: 1, rows: [{ secret_nonce: sealed.nonce, secret_ciphertext: sealed.ciphertext }] } : { rowCount: 0, rows: [] };
    if (sql.startsWith("SELECT a.slug")) return { rowCount: 1, rows: [{
      slug: "agent", active_release_id: manifest ? "rel_0123456789abcdef0123456789abcdef" : null,
      user_status: "active", secret_nonce: sealed?.nonce ?? null, secret_ciphertext: sealed?.ciphertext ?? null,
      previous_expires_at: rotation?.expiresAt ?? null, rotation_revision: rotation?.revision ?? null,
      rotated_at: rotation?.at ?? null, rotation_applied: applied, rotation_failed: !!rotation,
    }] };
    if (sql.startsWith("UPDATE cantelop_control.app_integration_rotations")) { writes.retry = values; return { rowCount: 1, rows: [] }; }
    if (sql.startsWith("SELECT artifact.manifest"))
      return manifest ? { rowCount: 1, rows: [{ manifest }] } : { rowCount: 0, rows: [] };
    if (sql.startsWith("UPDATE cantelop_control.app_config_values")) { writes.token = values; return { rowCount: 1, rows: [] }; }
    if (sql.startsWith("INSERT INTO cantelop_control.app_integration_rotations")) { writes.rotation = values; return { rowCount: 1, rows: [] }; }
    if (sql.startsWith("UPDATE cantelop_control.apps")) { writes.revision = values; return { rowCount: 1, rows: [] }; }
    if (sql.startsWith("UPDATE cantelop_control.operations")) return { rowCount: 0, rows: [] };
    if (sql.startsWith("INSERT INTO cantelop_control.operations")) { writes.operation = values; return { rowCount: 1, rows: [] }; }
    if (sql.startsWith("SELECT slug")) return { rowCount: 1, rows: [{ slug: "agent" }] };
    throw new Error(`unexpected query ${sql}`);
  });
  return { pool, writes };
}

test("rotation replaces the token and keeps the previous one for the grace period", async () => {
  const cipher = new ConfigCipher(Buffer.alloc(32, 9));
  const at = new Date("2026-10-13T12:00:00Z");
  const manifest = { kind: "cantelop-protocol-edge", optional_bindings: ["CANTELOP_INTEGRATION_TOKEN_PREVIOUS", "CANTELOP_INTEGRATION_TOKEN_PREVIOUS_EXPIRES_AT"] };
  const { pool, writes } = rotationPool({ cipher, manifest });
  const rotated = await new ConfigRepository(pool, cipher, () => at).rotateIntegrationForUser(userID, appID, 3600);
  expect(rotated).toEqual({
    app_id: appID, slug: "agent", edge_url: "https://agent.cantelop.dev", token: rotated.token,
    previous_token_expires_at: "2026-10-13T13:00:00.000Z",
    rotation: { revision: 5, rotated_at: at.toISOString(), status: "pending" },
  });
  expect(rotated.token).toMatch(/^[0-9a-f]{64}$/);
  expect(rotated.token).not.toBe("a".repeat(64));
  expect(cipher.open(appID, integrationName, writes.token[2], writes.token[3])).toBe(rotated.token);
  expect(cipher.open(appID, previousName, writes.rotation[1], writes.rotation[2])).toBe("a".repeat(64));
  expect(writes.rotation.slice(3)).toEqual([new Date("2026-10-13T13:00:00Z"), 5, at]);
  expect(writes.revision.slice(1, 2)).toEqual([5]);
  expect(JSON.parse(writes.operation[4])).toEqual({ revision: 5 });
  expect(pool.statements.at(-1).sql).toBe("COMMIT");
});

test("immediate rotation drops the previous token and needs no rotation-aware Edge", async () => {
  const cipher = new ConfigCipher(Buffer.alloc(32, 9));
  const { pool, writes } = rotationPool({ cipher, manifest: { kind: "cantelop-protocol-edge" } });
  const rotated = await new ConfigRepository(pool, cipher).rotateIntegrationForUser(userID, appID, 0);
  expect(rotated.previous_token_expires_at).toBeNull();
  expect(writes.rotation.slice(1, 4)).toEqual([null, null, null]);
  expect(pool.statements.some(({ sql }) => sql.startsWith("SELECT artifact.manifest"))).toBe(false);
});

test("rotation refuses a grace period the live Edge would ignore, and Apps without a credential", async () => {
  const cipher = new ConfigCipher(Buffer.alloc(32, 9));
  const old = rotationPool({ cipher, manifest: { kind: "cantelop-protocol-edge" } });
  await expect(new ConfigRepository(old.pool, cipher).rotateIntegrationForUser(userID, appID, 60))
    .rejects.toMatchObject({ code: "integration_rotation_unsupported" });
  expect(old.writes).toEqual({});
  expect(old.pool.statements.at(-1).sql).toBe("ROLLBACK");
  const missing = rotationPool({ cipher, token: null });
  await expect(new ConfigRepository(missing.pool, cipher).rotateIntegrationForUser(userID, appID, 60))
    .rejects.toMatchObject({ code: "integration_not_configured" });
  for (const grace of [-1, 1.5, 7 * 24 * 3600 + 1, Number.NaN])
    await expect(new ConfigRepository(old.pool, cipher).rotateIntegrationForUser(userID, appID, grace)).rejects.toThrow(TypeError);
  // A live release from before SDK 1.0 cannot honour a grace period either.
  const preSDKOne = rotationPool({ cipher, manifest: { kind: "cantelop-edge-api", schema_version: 3 } });
  await expect(new ConfigRepository(preSDKOne.pool, cipher).rotateIntegrationForUser(userID, appID, 60))
    .rejects.toMatchObject({ code: "integration_rotation_unsupported" });
  // An App without a live release takes the new token at its first activation.
  const { pool } = rotationPool({ cipher, manifest: undefined });
  await new ConfigRepository(pool, cipher).rotateIntegrationForUser(userID, appID, 60);
});

test("projection adds the previous token only while its grace period lasts", async () => {
  const cipher = new ConfigCipher(Buffer.alloc(32, 9));
  const token = cipher.seal(appID, integrationName, "a".repeat(64));
  const previous = cipher.seal(appID, previousName, "b".repeat(64));
  const now = new Date("2026-10-13T12:00:00Z");
  const expiresAt = new Date("2026-10-14T12:00:00Z");
  const pool = fakePool((sql, values = []) => {
    if (sql.startsWith("select") && sql.includes('from "cantelop_control"."apps"')) return { rowCount: 1, rows: [{ config_revision: "5" }] };
    if (sql.startsWith("select") && sql.includes('from "cantelop_control"."app_config_values"'))
      return { rowCount: 1, rows: [{ name: integrationName, kind: "secret", secret_nonce: token.nonce, secret_ciphertext: token.ciphertext }] };
    if (sql.includes("app_integration_rotations")) {
      expect(sql).toContain("previous_expires_at > $2");
      return values[1] < expiresAt
        ? { rowCount: 1, rows: [{ previous_nonce: previous.nonce, previous_ciphertext: previous.ciphertext, previous_expires_at: expiresAt }] }
        : { rowCount: 0, rows: [] };
    }
    throw new Error(`unexpected query ${sql}`);
  });
  const during = await new ConfigRepository(pool, cipher, () => now).projection(appID);
  expect(during.systemBindings.slice(2)).toEqual([
    { name: previousName, kind: "secret", value: "b".repeat(64) },
    { name: "CANTELOP_INTEGRATION_TOKEN_PREVIOUS_EXPIRES_AT", kind: "variable", value: "2026-10-14T12:00:00.000Z" },
  ]);
  const after = await new ConfigRepository(pool, cipher, () => expiresAt).projection(appID);
  expect(after.systemBindings.map(({ name }) => name)).toEqual([integrationName, "CANTELOP_DEFAULT_KEEP_ALIVE_SECONDS"]);
});

test("credentials report the token, live grace period and rotation propagation", async () => {
  const cipher = new ConfigCipher(Buffer.alloc(32, 9));
  const sealed = cipher.seal(appID, integrationName, "c".repeat(64));
  const rotatedAt = new Date("2026-10-13T12:00:00Z");
  const row = {
    slug: "agent", active_release_id: "rel_0123456789abcdef0123456789abcdef", user_status: "active",
    secret_nonce: sealed.nonce, secret_ciphertext: sealed.ciphertext, previous_expires_at: null,
    rotation_revision: null, rotated_at: null, rotation_applied: null, rotation_failed: null,
  };
  const read = async (overrides) => {
    const pool = fakePool((sql, values) => {
      expect(sql).toContain("FROM cantelop_control.apps a");
      expect(sql).toContain("o.progress = 'edge_config_applied' AND (o.input->>'revision')::bigint >= r.config_revision");
      expect(values.slice(0, 2)).toEqual([appID, userID]);
      return { rowCount: 1, rows: [{ ...row, ...overrides }] };
    });
    return new ConfigRepository(pool, cipher).integrationCredentialsForUser(userID, appID);
  };
  expect(await read({})).toEqual({
    app_id: appID, slug: "agent", edge_url: "https://agent.cantelop.dev", token: "c".repeat(64),
    previous_token_expires_at: null, rotation: null,
  });
  const rotation = { rotation_revision: "5", rotated_at: rotatedAt, previous_expires_at: new Date("2026-10-14T12:00:00Z") };
  expect(await read({ ...rotation, rotation_applied: false, rotation_failed: null })).toMatchObject({
    previous_token_expires_at: "2026-10-14T12:00:00.000Z",
    rotation: { revision: 5, rotated_at: rotatedAt.toISOString(), status: "pending" },
  });
  expect((await read({ ...rotation, rotation_applied: true, rotation_failed: true })).rotation.status).toBe("applied");
  expect((await read({ ...rotation, rotation_applied: false, rotation_failed: true })).rotation.status).toBe("failed");
  expect((await read({ ...rotation, active_release_id: null, rotation_applied: false })).rotation.status).toBe("applied");
  await expect(read({ secret_nonce: null, secret_ciphertext: null })).rejects.toMatchObject({ code: "integration_not_configured" });
  await expect(read({ user_status: "suspended" })).rejects.toMatchObject({ code: "user_suspended" });
});


test("an unresolved rotation must be retried without replacing either token", async () => {
  const cipher = new ConfigCipher(Buffer.alloc(32, 9));
  const manifest = { kind: "cantelop-protocol-edge", optional_bindings: [previousName, "CANTELOP_INTEGRATION_TOKEN_PREVIOUS_EXPIRES_AT"] };
  const rotation = { revision: "4", at: new Date("2026-10-13T12:00:00Z"), expiresAt: new Date("2026-10-14T12:00:00Z") };
  const { pool, writes } = rotationPool({ cipher, manifest, rotation });
  const repository = new ConfigRepository(pool, cipher);
  await expect(repository.rotateIntegrationForUser(userID, appID, 86400))
    .rejects.toMatchObject({ code: "integration_rotation_unresolved" });
  expect(writes).toEqual({});
  const retry = await repository.rotateIntegrationForUser(userID, appID, "retry");
  expect(retry.token).toBe("a".repeat(64));
  expect(retry.previous_token_expires_at).toBe(rotation.expiresAt.toISOString());
  expect(retry.rotation).toEqual({ revision: 5, rotated_at: rotation.at.toISOString(), status: "pending" });
  expect(writes.token).toBeUndefined();
  expect(writes.rotation).toBeUndefined();
  expect(writes.retry).toEqual([appID, 5]);
  expect(JSON.parse(writes.operation[4])).toEqual({ revision: 5 });
  // Once Edge accepts the token, another rotation may replace it normally.
  const applied = rotationPool({ cipher, manifest, rotation, applied: true });
  await new ConfigRepository(applied.pool, cipher).rotateIntegrationForUser(userID, appID, 86400);
  expect(applied.writes.token).toBeDefined();
});

test("retry requires an existing rotation", async () => {
  const cipher = new ConfigCipher(Buffer.alloc(32, 9));
  const { pool, writes } = rotationPool({ cipher });
  await expect(new ConfigRepository(pool, cipher).rotateIntegrationForUser(userID, appID, "retry"))
    .rejects.toMatchObject({ code: "conflict" });
  expect(writes).toEqual({});
});

test('App-scoped updates reuse encrypted storage, enforce declarations and return only revision metadata', async () => {
  const manifest = declaringEdgeManifest({ GH_TOKEN: { secret: true }, REQUIRED: { secret: true, required: true }, REGION: { required: true, default: 'us' } });
  const pool = fakePool(sql => {
    if (sql.startsWith('SELECT pg_advisory_xact_lock')) return { rowCount: 1, rows: [{}] };
    if (sql.startsWith('SELECT a.config_revision::text, a.owner_user_id')) return { rowCount: 1, rows: [{ config_revision: '8', owner_user_id: userID, user_status: 'active' }] };
    if (sql.startsWith('SELECT r.id, r.version')) return { rowCount: 1, rows: [{ id: releaseID, version: 1, manifest }] };
    if (sql.startsWith('SELECT kind')) return { rowCount: 0, rows: [] };
    if (sql.startsWith('SELECT count')) return { rowCount: 1, rows: [{ entries: '0', bytes: '0' }] };
    if (sql.startsWith('UPDATE') || sql.startsWith('INSERT')) return { rowCount: 1, rows: [] };
    throw new Error(`unexpected query ${sql}`);
  });
  const repository = new ConfigRepository(pool, new ConfigCipher(Buffer.alloc(32, 9)));
  const queryOutsideTransaction = pool.query;
  pool.query = (sql, values) => {
    if (sql.startsWith('SELECT r.id, r.version')) throw new Error('declaration checks must reuse the transaction connection');
    return queryOutsideTransaction(sql, values);
  };
  expect(await repository.setForApp(appID, 'secret', 'GH_TOKEN', 'rotation-token')).toEqual({ revision: 9, status: 'pending' });
  const insert = pool.statements.find(row => row.sql.startsWith('INSERT INTO cantelop_control.app_config_values'));
  expect(insert.values[3]).toBeNull();
  expect(insert.values[5].includes(Buffer.from('rotation-token'))).toBe(false);
  const before = pool.statements.filter(row => row.sql.startsWith('INSERT')).length;
  for (const [kind, name] of [['secret', 'UNDECLARED'], ['variable', 'GH_TOKEN'], ['secret', 'CANTELOP_INTEGRATION_TOKEN']]) {
    await expect(repository.setForApp(appID, kind, name, 'token')).rejects.toThrow();
  }
  await expect(repository.deleteForApp(appID, 'secret', 'REQUIRED')).rejects.toThrow();
  await expect(repository.setForApp(appID, 'secret', 'REQUIRED', '')).rejects.toThrow();
  await expect(repository.setForApp(appID, 'variable', 'REGION', '')).rejects.toThrow();
  expect(pool.statements.filter(row => row.sql.startsWith('INSERT')).length).toBe(before);
  expect(await repository.deleteForApp(appID, 'secret', 'GH_TOKEN')).toEqual({ revision: 8, status: 'applied' });
  expect(await repository.deleteForApp(appID, 'variable', 'REGION')).toEqual({ revision: 8, status: 'applied' });
});

test.each([
  [{ revision: '9', applied: false, failed: false }, 'pending'],
  [{ revision: '10', applied: true, failed: false }, 'applied'],
  [{ revision: '9', applied: false, failed: true }, 'failed'],
])('App configuration status reports projection completion without returning values (%j)', async (row, status) => {
  const pool = fakePool(sql => {
    expect(sql).toContain("o.kind = 'sync_config'");
    expect(sql).toContain("u.status = 'active'");
    return { rowCount: 1, rows: [row] };
  });
  const repository = new ConfigRepository(pool, new ConfigCipher(Buffer.alloc(32, 9)));
  expect(await repository.environmentStatus(appID, 9)).toEqual({ revision: 9, status });
  await expect(repository.environmentStatus(appID, 11)).rejects.toThrow();
});
