import { expect, test } from "bun:test";
import { spawnSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { resolve } from "node:path";
import pg from "pg";

import { observabilityDatabaseConfig } from "./database";
import { parseObservationBatch } from "./events";
import { ObservationRejectedError, ObservationRepository } from "./repository";

const databaseURL = process.env.CANTELOP_TEST_DATABASE_URL;
const postgresTest = databaseURL ? test : test.skip;

postgresTest("dispatcher lifecycle and SDK spans produce a complete PostgreSQL trace", async () => {
  const directory = mkdtempSync(resolve(tmpdir(), "cantelop-trace-contract-"));
  const fixture = resolve(directory, "batches.json");
  const sslMode = ["localhost", "127.0.0.1", "::1"].includes(new URL(databaseURL).hostname) ? "disable" : "verify-full";
  const pool = new pg.Pool(observabilityDatabaseConfig({
    CANTELOP_DATABASE_URL: databaseURL, CANTELOP_DATABASE_SSL: sslMode,
  }));
  try {
    const generated = spawnSync("go", ["test", "./fire-fuse/dispatcher", "-run", "^TestExecutionTraceContract$", "-count=1"], {
      cwd: resolve(import.meta.dir, "../../.."),
      env: { ...process.env, CANTELOP_TEST_TRACE_BATCHES: fixture }, encoding: "utf8", timeout: 120_000,
    });
    expect(generated.status, generated.stdout + generated.stderr).toBe(0);
    const batches = JSON.parse(readFileSync(fixture, "utf8")).map(parseObservationBatch);
    const accepted = batches[0].events[0];
    const repository = ObservationRepository.fromPool(pool);
    const at = new Date(accepted.occurred_at);
    // Clear only this contract fixture in the explicitly configured test database.
    const client = await pool.connect();
    try {
      await client.query("BEGIN");
      // A separate schema is unnecessary; fixed fixture identities are cleaned
      // only in the explicitly configured test database, never production.
      await client.query("DELETE FROM cantelop_observability.sessions WHERE app_id = $1", [accepted.app_id]);
      await client.query("DELETE FROM cantelop_observability.requests WHERE app_id = $1", [accepted.app_id]);
      const ids = batches.flatMap(({ events }) => events.map(({ event_id }) => event_id));
      await client.query("DELETE FROM cantelop_observability.ingestion_receipts WHERE event_id = ANY($1::text[])", [ids]);
      await client.query("COMMIT");
    } finally { client.release(); }

    // Reproduce the production error: spans cannot arrive before their attempt.
    await expect(repository.ingest(batches[1], at)).rejects.toBeInstanceOf(ObservationRejectedError);
    for (const batch of batches) {
      expect((await repository.ingest(batch, at)).accepted).toBe(batch.events.length);
    }
    // A recovered dispatcher resends the same lifecycle identities safely.
    expect(await repository.ingest(batches[0], at)).toEqual({ accepted: 0, duplicated: 3 });
    const request = parseObservationBatch({ events: [{
      schema_version: 1, source: "edge", event_id: "trace-contract:request", type: "request.completed",
      app_id: accepted.app_id, request_id: accepted.request_id, trace_id: accepted.request_id.slice(4),
      occurred_at: accepted.occurred_at, received_at: accepted.occurred_at, completed_at: accepted.occurred_at,
      method: "POST", path: "/slack/events", status_code: 202,
    }] });
    await pool.query("DELETE FROM cantelop_observability.ingestion_receipts WHERE source = 'edge' AND event_id = 'trace-contract:request'");
    await repository.ingest(request, at);
    expect(await repository.routeTraffic(accepted.app_id, [{ method: "POST", path: "/slack/events" }])).toEqual([{
      method: "POST", path: "/slack/events", requests: 1, clientErrors: 0, serverErrors: 0,
      medianDurationUs: null, lastSeenAt: new Date(accepted.occurred_at), messages: 1, failedAttempts: 0,
    }]);
    const tree = await repository.traceTree(accepted.app_id);
    expect(tree.unlinked_messages).toHaveLength(0);
    expect(tree.requests).toHaveLength(1);
    const message = tree.requests[0].messages[0];
    expect(message.id).toBe(accepted.message_id);
    expect(message.session.logical_session_id).toBe("slack:test");
    expect(message.attempts).toHaveLength(1);
    expect(message.attempts[0].outcome).toBe("completed");
    expect(message.attempts[0].sandbox.id).toBe("sbx-22222222222222222222222222222222");
    expect(message.attempts[0].spans).toMatchObject([{ name: "session.receive", status: "ok" }]);
  } finally {
    await pool.end();
    rmSync(directory, { recursive: true, force: true });
  }
}, 120_000);
