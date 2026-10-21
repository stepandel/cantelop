import { expect, test } from "bun:test";

import { withTransaction } from "./transaction";

function fakePool(failOn = {}) {
  const pool = { statements: [], released: [] };
  const client = {
    async query(sql) {
      pool.statements.push(sql);
      if (failOn[sql]) throw failOn[sql];
      return { rows: [], rowCount: null };
    },
    release(error) { pool.released.push(error); },
  };
  pool.connect = async () => client;
  return pool;
}

test("commits the operation's result and returns the client to the pool", async () => {
  const pool = fakePool();
  expect(await withTransaction(pool, async (client) => {
    await client.query("SELECT 1");
    return 42;
  }, { lockTimeoutMilliseconds: 5_000 })).toBe(42);
  expect(pool.statements).toEqual(["BEGIN", "SET LOCAL lock_timeout = '5000ms'", "SELECT 1", "COMMIT"]);
  expect(pool.released).toEqual([undefined]);
});

test("rolls back and rethrows the operation's error", async () => {
  const pool = fakePool();
  const failure = new Error("conflict");
  await expect(withTransaction(pool, async () => { throw failure; })).rejects.toBe(failure);
  expect(pool.statements).toEqual(["BEGIN", "ROLLBACK"]);
  expect(pool.released).toEqual([undefined]);
});

test("a failed rollback neither masks the original error nor returns the client", async () => {
  const lost = new Error("connection terminated");
  const pool = fakePool({ ROLLBACK: lost });
  const failure = new Error("conflict");
  await expect(withTransaction(pool, async () => { throw failure; })).rejects.toBe(failure);
  expect(pool.released).toEqual([lost]);
});

test("a failed BEGIN discards the client without attempting a rollback", async () => {
  const lost = new Error("connection terminated");
  const pool = fakePool({ BEGIN: lost });
  await expect(withTransaction(pool, async () => 1)).rejects.toBe(lost);
  expect(pool.statements).toEqual(["BEGIN"]);
  expect(pool.released).toEqual([lost]);
});

test("rejects malformed lock timeouts before connecting", async () => {
  const pool = fakePool();
  await expect(withTransaction(pool, async () => 1, { lockTimeoutMilliseconds: 1.5 })).rejects.toBeInstanceOf(TypeError);
  expect(pool.statements).toEqual([]);
});
