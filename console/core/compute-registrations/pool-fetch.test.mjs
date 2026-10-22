import { expect, test } from "bun:test";
import { computePoolFetch } from "./pool-fetch";

const app = "app_" + "a".repeat(32), user = "usr_" + "b".repeat(32);
const environment = { CANTELOP_COMPUTE_INGRESS_URL: "http://127.0.0.1:8084", CANTELOP_COMPUTE_CONTROL_TOKEN: "c".repeat(64),
  CANTELOP_MANAGED_COMPUTE_INGRESS_URL: "http://127.0.0.1:8184", CANTELOP_MANAGED_COMPUTE_CONTROL_TOKEN: "d".repeat(64) };
const url = `${environment.CANTELOP_COMPUTE_INGRESS_URL}/v1/internal/users/${user}/apps/${app}/configuration`;
const command = () => new Request(url, { method: "PUT", headers: { Authorization: `Bearer ${environment.CANTELOP_COMPUTE_CONTROL_TOKEN}` }, body: "configuration" });
test("App placement routes control requests and credentials to separate runtimes", async () => {
  let placement = "standard";
  const requests = [];
  const routed = computePoolFetch({ async query(_sql, values) { expect(values).toEqual([app]); return { rows: [{ compute_pool: placement }] }; } }, environment,
    async request => { requests.push({ url: request.url, token: request.headers.get("authorization"), body: await request.text() }); return new Response(); });
  await routed(command());
  placement = "managed";
  await routed(command());
  expect(requests).toEqual([
    { url, token: `Bearer ${environment.CANTELOP_COMPUTE_CONTROL_TOKEN}`, body: "configuration" },
    { url: url.replace(":8084", ":8184"), token: `Bearer ${environment.CANTELOP_MANAGED_COMPUTE_CONTROL_TOKEN}`, body: "configuration" },
  ]);
});
test("missing placement or managed configuration cannot fall back to ordinary compute", async () => {
  let placement = "managed", calls = 0;
  const db = { async query() { return { rows: placement ? [{ compute_pool: placement }] : [] }; } };
  const request = async () => { calls++; return new Response(); };
  const routed = computePoolFetch(db, { ...environment, CANTELOP_MANAGED_COMPUTE_CONTROL_TOKEN: undefined }, request);
  await expect(routed(command())).rejects.toThrow("managed_compute_unavailable");
  placement = "";
  await expect(routed(command())).rejects.toThrow("compute_placement_unavailable");
  placement = "unknown";
  await expect(routed(command())).rejects.toThrow("compute_placement_unavailable");
  await expect(routed("http://outside.example/")).rejects.toThrow("invalid_compute_request");
  expect(calls).toBe(0);
});
