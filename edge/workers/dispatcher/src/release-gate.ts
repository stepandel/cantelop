import { assertAPIInvocation, assertAppId, type APIInvocation } from "@cantelop/edge-contracts";

import type { UserWorkerNamespace } from "./core";

export const GATE_INVOCATION_HEADER = "x-cantelop-release-gate-invocation";
export const GATE_APP_HEADER = "x-cantelop-release-gate-app";
const controlOrigin = "https://release-gate.internal";
const generationPattern = /^act_[0-9a-f]{32}$/;
const scriptPattern = /^(?:app|rel)_[0-9a-f]{32}$/;

export interface ReleaseGateState {
  app_id: string;
  generation: string;
  closed: boolean;
  script_name: string | null;
}

interface GateEnvironment {
  USER_WORKERS: UserWorkerNamespace;
}

// The selected Worker and admission flag live together in one strongly
// consistent Durable Object. Closing the gate waits for requests admitted by
// this object before acknowledging the control operation.
export class ReleaseGate {
  private route: ReleaseGateState | null = null;
  private readonly inFlight = new Set<Promise<Response>>();
  private controlTail: Promise<void> = Promise.resolve();

  constructor(private readonly state: DurableObjectState, private readonly env: GateEnvironment) {
    state.blockConcurrencyWhile(async () => {
      this.route = await state.storage.get<ReleaseGateState>("route") ?? null;
    });
  }

  async fetch(request: Request): Promise<Response> {
    if (new URL(request.url).origin === controlOrigin) return this.serialControl(request);
    const route = this.route;
    if (!route || route.closed || !route.script_name) return unavailable();
    let invocation: APIInvocation;
    try {
      if (request.headers.get(GATE_APP_HEADER) !== route.app_id) return unavailable();
      const parsed: unknown = JSON.parse(request.headers.get(GATE_INVOCATION_HEADER) ?? "");
      assertAPIInvocation(parsed);
      invocation = parsed;
      if (invocation.app_id !== route.app_id) return unavailable();
    } catch {
      return unavailable();
    }
    const headers = new Headers(request.headers);
    headers.delete(GATE_APP_HEADER);
    headers.delete(GATE_INVOCATION_HEADER);
    const forwarded = new Request(request.url, {
      method: request.method,
      headers,
      body: request.method === "GET" || request.method === "HEAD" ? null : request.body,
      redirect: request.redirect,
    });
    const call = Promise.resolve().then(() => this.env.USER_WORKERS.get(
      route.script_name!, {}, { outbound: { CANTELOP_CONTEXT: invocation } },
    ).fetch(forwarded));
    this.inFlight.add(call);
    try { return await call; }
    finally { this.inFlight.delete(call); }
  }

  private async serialControl(request: Request): Promise<Response> {
    const previous = this.controlTail;
    let release!: () => void;
    this.controlTail = new Promise<void>((resolve) => { release = resolve; });
    await previous;
    try { return await this.control(request); }
    finally { release(); }
  }

  private async control(request: Request): Promise<Response> {
    if (request.method === "GET") return Response.json(this.route ?? { closed: true, generation: null, script_name: null });
    if (request.method !== "PUT") return new Response(null, { status: 405 });
    let command: { app_id?: unknown; generation?: unknown; action?: unknown; script_name?: unknown };
    try { command = await request.json() as typeof command; }
    catch { return new Response(null, { status: 400 }); }
    try { assertAppId(command.app_id); }
    catch { return new Response(null, { status: 400 }); }
    if (typeof command.generation !== "string" || !generationPattern.test(command.generation) ||
        !["close", "select", "open"].includes(String(command.action))) return new Response(null, { status: 400 });
    const appID = command.app_id;
    const generation = command.generation;
    const current = this.route;
    if (current && current.app_id !== appID) return new Response(null, { status: 409 });
    if (command.action === "close") {
      if (current && generation < current.generation) {
        return new Response(null, { status: 409 });
      }
      const next = { app_id: appID, generation, closed: true, script_name: current?.script_name ?? null };
      this.route = next;
      try { await this.state.storage.put("route", next); }
      catch { return unavailable(); }
      await Promise.allSettled([...this.inFlight]);
      return Response.json(next);
    }
    if (!current || current.generation !== generation || !current.closed) return new Response(null, { status: 409 });
    if (command.action === "select") {
      if (typeof command.script_name !== "string" || !scriptPattern.test(command.script_name)) return new Response(null, { status: 400 });
      const next = { ...current, script_name: command.script_name };
      try { await this.state.storage.put("route", next); }
      catch { return unavailable(); }
      this.route = next;
      return Response.json(next);
    }
    if (!current.script_name) return new Response(null, { status: 409 });
    const next = { ...current, closed: false };
    try { await this.state.storage.put("route", next); }
    catch { return unavailable(); }
    this.route = next;
    return Response.json(next);
  }
}

function unavailable(): Response {
  return Response.json({ error: { code: "release_transition" } },
    { status: 503, headers: { "Cache-Control": "no-store" } });
}
