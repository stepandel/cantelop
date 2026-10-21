import { assertAppId, publishObservations, type ObservationIngestEnv } from "@cantelop/edge-contracts";

import { dispatchPublicRequest, type DispatcherEnv } from "./core";
export { ReleaseGate } from "./release-gate";

type Environment = DispatcherEnv & ObservationIngestEnv & { RELEASE_GATE_TOKEN?: string };

const controlPath = /^\/__cantelop\/release-gates\/(app_[0-9a-f]{32})$/;

async function releaseGateControl(request: Request, env: Environment, appID: string): Promise<Response> {
  if (!env.RELEASE_GATE_TOKEN) return new Response(null, { status: 503 });
  if (request.headers.get("Authorization") !== `Bearer ${env.RELEASE_GATE_TOKEN}`) {
    return new Response(null, { status: 401 });
  }
  const gate = env.RELEASE_GATES.get(env.RELEASE_GATES.idFromName(appID));
  if (request.method === "GET") return gate.fetch("https://release-gate.internal/");
  if (request.method !== "PUT" || request.headers.get("Content-Type") !== "application/json") {
    return new Response(null, { status: 405 });
  }
  let command: Record<string, unknown>;
  try {
    const body = await readBoundedCommand(request);
    command = JSON.parse(body) as Record<string, unknown>;
    if (!command || typeof command !== "object" || Array.isArray(command)) throw new Error("invalid command");
  } catch {
    return new Response(null, { status: 400 });
  }
  return gate.fetch(new Request("https://release-gate.internal/", {
    method: "PUT", headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ ...command, app_id: appID }),
  }));
}

async function readBoundedCommand(request: Request): Promise<string> {
  if (!request.body) throw new Error("empty command");
  const reader = request.body.getReader();
  const chunks: Uint8Array[] = [];
  let length = 0;
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      length += value.byteLength;
      if (length > 512) throw new Error("command too large");
      chunks.push(value);
    }
  } finally {
    await reader.cancel().catch(() => {});
    reader.releaseLock();
  }
  const body = new Uint8Array(length);
  let offset = 0;
  for (const chunk of chunks) { body.set(chunk, offset); offset += chunk.byteLength; }
  return new TextDecoder("utf-8", { fatal: true, ignoreBOM: false }).decode(body);
}

export default {
  fetch(request: Request, env: Environment, context: ExecutionContext): Promise<Response> {
    const url = new URL(request.url);
    if (url.hostname === "release-gate.cantelop.dev") {
      const match = controlPath.exec(url.pathname);
      if (match && !url.search) {
        try { assertAppId(match[1]); }
        catch { return Promise.resolve(new Response(null, { status: 404 })); }
        return releaseGateControl(request, env, match[1]);
      }
    }
    return dispatchPublicRequest(request, env, undefined, undefined, (observation) => {
      context.waitUntil(publishObservations(env, [observation], {
        userAgent: "cantelop-edge-observability/1",
      }).catch((error: unknown) => {
        console.error("could not publish completed request observation", error);
      }));
    });
  },
} satisfies ExportedHandler<Environment>;
