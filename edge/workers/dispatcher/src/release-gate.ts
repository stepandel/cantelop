import { assertAPIInvocation, assertAppId, type APIInvocation } from "@cantelop/edge-contracts";

import type { UserWorkerNamespace } from "./core";

export const GATE_INVOCATION_HEADER = "x-cantelop-release-gate-invocation";
export const GATE_APP_HEADER = "x-cantelop-release-gate-app";
const controlOrigin = "https://release-gate.internal";
const generationPattern = /^act_[0-9a-f]{32}$/;
const scriptPattern = /^(?:app|rel|pkg)_[0-9a-f]{32}$/;

export interface ReleaseGateState {
  app_id: string;
  generation: string;
  closed: boolean;
  script_name: string | null;
  configuration_revision?: number;
  retired?: true;
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
  private configuration: { revision: number; bindings: Record<string, string> } | null = null;
  private readonly activeInvocations = new Set<string>();
  private readonly streams = new Map<Promise<void>, () => Promise<void>>();

  constructor(private readonly state: DurableObjectState, private readonly env: GateEnvironment) {
    state.blockConcurrencyWhile(async () => {
      this.route = await state.storage.get<ReleaseGateState>("route") ?? null;
      const meta = await state.storage.get<{ revision: number; names: string[] }>("configuration");
      if (meta?.names) {
        const values = new Map<string, string>();
        for (let i = 0; i < meta.names.length; i += 64) {
          for (const [key, value] of await state.storage.get<string>(meta.names.slice(i, i + 64).map(name => `configuration:${name}`))) values.set(key, value);
        }
        if (values.size !== meta.names.length) throw new Error("incomplete Fleet configuration");
        this.configuration = { revision: meta.revision, bindings: Object.fromEntries(
          meta.names.map(name => [name, values.get(`configuration:${name}`)!])) };
      }
    });
  }

  async fetch(request: Request): Promise<Response> {
    if (new URL(request.url).origin === controlOrigin) {
      if (new URL(request.url).pathname === "/authorize" && request.method === "POST") {
        const value = await request.json() as Record<string, unknown>;
        return new Response(null, { status: this.route && !this.route.closed &&
          value.app_id === this.route.app_id && value.package_id === this.route.script_name &&
          value.generation === this.route.generation && value.configuration_revision === this.configuration?.revision &&
          (value.request_id === undefined || this.activeInvocations.has(String(value.request_id))) ? 204 : 403 });
      }
      return this.serialControl(request);
    }
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
    const configuration = this.configuration;
    const fleet = route.script_name!.startsWith("pkg_");
    if (fleet) this.activeInvocations.add(invocation.request_id);
    const call = Promise.resolve().then(() => {
      if (route.script_name!.startsWith("pkg_")) {
        const exports = this.state.exports as unknown as { FleetRunner?: (options: { props: unknown }) => Fetcher };
        if (!exports.FleetRunner || !configuration) return unavailable();
        return exports.FleetRunner({ props: { execution: { app_id: route.app_id, package_id: route.script_name!,
          generation: route.generation, configuration_revision: configuration.revision,
          bindings: configuration.bindings }, invocation } }).fetch(forwarded);
      }
      return this.env.USER_WORKERS.get(route.script_name!, {}, { outbound: { CANTELOP_CONTEXT: invocation } }).fetch(forwarded);
    });
    this.inFlight.add(call);
    try {
      const response = await call;
      if (fleet && (this.route?.closed || this.route?.generation !== route.generation ||
          this.route?.script_name !== route.script_name || this.configuration?.revision !== configuration?.revision)) {
        this.activeInvocations.delete(invocation.request_id);
        void response.body?.cancel("release transition").catch(() => {});
        if (response.webSocket) {
          try { response.webSocket.accept(); response.webSocket.close(1012, "release transition"); } catch { /* Already closed. */ }
        }
        return unavailable();
      }
      if (fleet && response.webSocket) return this.trackWebSocket(response, invocation.request_id);
      if (!fleet || !response.body) {
        this.activeInvocations.delete(invocation.request_id);
        return response;
      }
      const reader = response.body.getReader();
      let resolve: () => void = () => {};
      const done = new Promise<void>(finish => { resolve = finish; });
      const finish = () => { this.streams.delete(done); this.activeInvocations.delete(invocation.request_id); resolve(); };
      const body = new ReadableStream<Uint8Array>({
        async pull(controller) {
          try {
            const value = await reader.read();
            if (value.done) { controller.close(); finish(); }
            else controller.enqueue(value.value);
          } catch (error) { controller.error(error); finish(); }
        },
        async cancel(reason) { try { await reader.cancel(reason); } finally { finish(); } },
      });
      this.streams.set(done, async () => { try { await reader.cancel("release transition"); } finally { finish(); } });
      return new Response(body, { status: response.status, statusText: response.statusText, headers: response.headers });
    } catch (error) { this.activeInvocations.delete(invocation.request_id); throw error; }
    finally { this.inFlight.delete(call); }
  }

  private trackWebSocket(response: Response, requestID: string): Response {
    const upstream = response.webSocket!;
    const pair = new WebSocketPair();
    const downstream = pair[1];
    upstream.accept();
    downstream.accept();
    let resolve: () => void = () => {};
    const done = new Promise<void>(finish => { resolve = finish; });
    const finish = () => { this.streams.delete(done); this.activeInvocations.delete(requestID); resolve(); };
    const stop = async () => {
      try { upstream.close(1012, "release transition"); } catch { /* Already closed. */ }
      try { downstream.close(1012, "release transition"); } catch { /* Already closed. */ }
      finish();
    };
    upstream.addEventListener("message", event => { try { downstream.send(event.data); } catch { void stop(); } });
    downstream.addEventListener("message", event => { try { upstream.send(event.data); } catch { void stop(); } });
    for (const [source, target] of [[upstream, downstream], [downstream, upstream]]) {
      source.addEventListener("close", event => {
        try { target.close(event.code === 1005 ? 1000 : event.code, event.reason); } catch { /* Peer already closed. */ }
        finish();
      });
      source.addEventListener("error", () => { void stop(); });
    }
    this.streams.set(done, stop);
    return new Response(null, { status: 101, headers: response.headers, webSocket: pair[0] });
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
    let command: { app_id?: unknown; generation?: unknown; action?: unknown; script_name?: unknown;
      revision?: unknown; bindings?: unknown };
    try { command = await request.json() as typeof command; }
    catch { return new Response(null, { status: 400 }); }
    try { assertAppId(command.app_id); }
    catch { return new Response(null, { status: 400 }); }
    const appID = command.app_id;
    const current = this.route;
    if (current && current.app_id !== appID) return new Response(null, { status: 409 });
    if (command.action === "purge") {
      if (!current?.closed || command.generation !== current.generation) return new Response(null, { status: 409 });
      const next: ReleaseGateState = { ...current, retired: true, script_name: null };
      await this.state.storage.transaction(async tx => {
        const keys = Object.keys(this.configuration?.bindings ?? {}).map(name => `configuration:${name}`);
        for (let i = 0; i < keys.length; i += 64) await tx.delete(keys.slice(i, i + 64));
        await tx.delete("configuration");
        await tx.put("route", next);
      });
      this.configuration = null;
      this.route = next;
      return Response.json(next);
    }
    if (current?.retired && command.action !== "close") return new Response(null, { status: 409 });
    if (command.action === "configure") {
      if (!current || !Number.isSafeInteger(command.revision) || (command.revision as number) < 1 ||
          !command.bindings || typeof command.bindings !== "object" || Array.isArray(command.bindings) ||
          Object.keys(command.bindings).length > 132 ||
          Object.entries(command.bindings).some(([name, value]) => !/^[A-Za-z_][A-Za-z0-9_]{0,127}$/.test(name) ||
            typeof value !== "string" || new TextEncoder().encode(value).length > 65_536)) {
        return new Response(null, { status: 400 });
      }
      const bindings = Object.fromEntries(Object.entries(command.bindings).sort(([a], [b]) => a.localeCompare(b))) as Record<string, string>;
      if (this.configuration && ((command.revision as number) < this.configuration.revision ||
          command.revision === this.configuration.revision && JSON.stringify(bindings) !== JSON.stringify(this.configuration.bindings))) {
        return new Response(null, { status: 409 });
      }
      const configuration = { revision: command.revision as number, bindings };
      const next = { ...current, configuration_revision: configuration.revision };
      await this.state.storage.transaction(async tx => {
        const old = Object.keys(this.configuration?.bindings ?? {}).map(name => `configuration:${name}`);
        for (let i = 0; i < old.length; i += 64) await tx.delete(old.slice(i, i + 64));
        const entries = Object.entries(bindings).map(([name, value]) => [`configuration:${name}`, value]);
        for (let i = 0; i < entries.length; i += 64) await tx.put(Object.fromEntries(entries.slice(i, i + 64)));
        await tx.put({ route: next, configuration: { revision: configuration.revision, names: Object.keys(bindings) } });
      });
      this.configuration = configuration;
      this.route = next;
      await Promise.allSettled([...this.streams.values()].map(stop => stop()));
      return Response.json(next);
    }
    if (typeof command.generation !== "string" || !generationPattern.test(command.generation) ||
        !["close", "select", "open"].includes(String(command.action))) return new Response(null, { status: 400 });
    const generation = command.generation;
    if (command.action === "close") {
      if (current && generation < current.generation) {
        return new Response(null, { status: 409 });
      }
      const next = { app_id: appID, generation, closed: true, script_name: current?.script_name ?? null,
        ...(current?.retired ? { retired: true as const } : {}),
        ...(this.configuration ? { configuration_revision: this.configuration.revision } : {}) };
      this.route = next;
      try { await this.state.storage.put("route", next); }
      catch { return unavailable(); }
      await Promise.allSettled([...this.streams.values()].map(stop => stop()));
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
    if (!current.script_name || current.script_name.startsWith("pkg_") && !this.configuration) return new Response(null, { status: 409 });
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
