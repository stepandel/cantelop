import { test, expect } from "bun:test";
import { TursoProvider, databaseName } from "./provider";
const workspace = "wsp_0123456789abcdef0123456789abcdef";
const name = databaseName("production", workspace);
const database = { Name: name, DbId: "provider-id", Hostname: `${name}-cantelop.turso.io`, group: "production" };
test("reconciles create conflict without duplicating a database", async () => {
  const calls = [];
  const provider = new TursoProvider("cantelop", "production", "secret", async (url, init) => {
    calls.push([url, init]); return calls.length === 1 ? new Response(null, { status: 409 }) : Response.json({ database });
  });
  expect(await provider.ensure(name)).toEqual({ id: "provider-id", url: `libsql://${database.Hostname}` });
  expect(calls.map(c => c[1].method)).toEqual(["POST", "GET"]);
});
test("rejects another group and suppresses provider error bodies", async () => {
  const provider = new TursoProvider("cantelop", "production", "secret", async () => Response.json({ database: { ...database, group: "other" } }));
  await expect(provider.ensure(name)).rejects.toMatchObject({ retryable: false });
  const rejected = new TursoProvider("cantelop", "production", "secret", async () => new Response("secret", { status: 401 }));
  await expect(rejected.ensure(name)).rejects.toMatchObject({ message: "workspace database provider unavailable", retryable: false });
});
test("network failures are retryable and repeated deletion accepts not found", async () => {
  const provider = new TursoProvider("cantelop", "production", "secret", async () => { throw new Error("secret"); });
  await expect(provider.ensure(name)).rejects.toMatchObject({ retryable: true });
  const deleted = new TursoProvider("cantelop", "production", "secret", async () => new Response(null, { status: 404 }));
  await deleted.delete(name);
});
test("tokens are single database and explicitly expire", async () => {
  const provider = new TursoProvider("cantelop", "production", "secret", async (url) => {
    expect(url).toContain(`/${name}/auth/tokens?expiration=15m&authorization=full-access`); return Response.json({ jwt: "database-token-for-test" });
  });
  expect(await provider.token(name)).toBe("database-token-for-test");
  expect(databaseName("production", workspace)).not.toBe(databaseName("staging", workspace));
});
test("lists the configured group across pages and ignores other groups", async () => {
  const urls = [];
  const provider = new TursoProvider("cantelop", "production", "secret", async (url, init) => {
    urls.push(url); expect(init.method).toBe("GET");
    return urls.length === 1
      ? Response.json({ databases: [database, { ...database, Name: "elsewhere", group: "other" }], pagination: { next: "page-2" } })
      : Response.json({ databases: [{ ...database, Name: "ct-production-second" }] });
  });
  expect(await provider.list()).toEqual([name, "ct-production-second"]);
  expect(urls).toEqual([
    "https://api.turso.tech/v1/organizations/cantelop/databases?group=production",
    "https://api.turso.tech/v1/organizations/cantelop/databases?group=production&cursor=page-2",
  ]);
  const malformed = new TursoProvider("cantelop", "production", "secret", async () => Response.json({ databases: "nope" }));
  await expect(malformed.list()).rejects.toMatchObject({ retryable: true });
});
