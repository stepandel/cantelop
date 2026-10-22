import { readFileSync } from "node:fs";

type Consumer = { service: string; environment?: string; namespace?: string };

export async function synchronizeTailConsumers(
  name: string,
  consumers: Consumer[],
  accountID: string,
  token: string,
  request: typeof fetch = fetch,
  delay: (milliseconds: number) => Promise<void> = (milliseconds) => new Promise(resolve => setTimeout(resolve, milliseconds)),
): Promise<void> {
  if (!/^[a-z0-9][a-z0-9-]{0,62}$/.test(name) || !/^[0-9a-f]{32}$/.test(accountID) || !token ||
      !Array.isArray(consumers) || consumers.length === 0 ||
      consumers.some((consumer) => !consumer || typeof consumer.service !== "string" || !consumer.service)) {
    throw new Error("invalid Worker tail settings inputs");
  }
  const endpoint = `https://api.cloudflare.com/client/v4/accounts/${accountID}/workers/scripts/${name}`;
  const headers = { authorization: `Bearer ${token}`, "content-type": "application/json" };
  const patch = await request(`${endpoint}/script-settings`, {
    method: "PATCH", headers, body: JSON.stringify({ tail_consumers: consumers }),
    redirect: "error", signal: AbortSignal.timeout(30_000),
  });
  const result = await patch.json() as { success?: boolean };
  if (!patch.ok || result.success !== true) throw new Error(`Worker tail settings failed with HTTP ${patch.status}`);
  const identity = (values: Consumer[]) => JSON.stringify(values.map((value) =>
    [value.service, value.environment ?? "", value.namespace ?? ""]).sort());
  // Settings writes propagate separately from version deployment. Require an
  // independent matching read, allowing a bounded window for stale replicas.
  for (let attempt = 0; attempt < 5; attempt++) {
    const readback = await request(`${endpoint}/settings`, {
      headers, redirect: "error", signal: AbortSignal.timeout(30_000),
    });
    const state = await readback.json() as { success?: boolean; result?: { tail_consumers?: Consumer[] } };
    if (!readback.ok || state.success !== true) throw new Error(`Worker tail settings readback failed with HTTP ${readback.status}`);
    if (Array.isArray(state.result?.tail_consumers) && identity(state.result.tail_consumers) === identity(consumers)) return;
    if (attempt < 4) await delay(250 * 2 ** attempt);
  }
  throw new Error("Worker tail settings readback mismatch");
}

if (import.meta.main) {
  const config = JSON.parse(readFileSync(process.argv[2], "utf8"));
  await synchronizeTailConsumers(config.name, config.tail_consumers,
    process.env.CLOUDFLARE_ACCOUNT_ID ?? "", process.env.CLOUDFLARE_API_TOKEN ?? "");
  console.log(`Verified Tail Worker attachment for ${config.name}`);
}
