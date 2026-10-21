import { assertResourceID } from "../domain/ids";
import { configuredAppDomain } from "../deployment";

const generationPattern = /^act_[0-9a-f]{32}$/;
const scriptPattern = /^(?:app|rel)_[0-9a-f]{32}$/;

export interface EdgeGateState {
  app_id?: string;
  generation: string | null;
  closed: boolean;
  script_name: string | null;
}

export class EdgeGateProvider {
  private readonly origin: string;

  constructor(config: { origin: string; token: string }, private readonly request: typeof fetch = fetch) {
    const origin = new URL(config.origin);
    if (origin.protocol !== "https:" || origin.username || origin.password || origin.pathname !== "/" ||
        origin.search || origin.hash || !/^[0-9a-f]{64}$/.test(config.token)) {
      throw new TypeError("invalid edge release gate configuration");
    }
    this.origin = origin.origin;
    this.token = config.token;
  }

  private readonly token: string;

  read(appID: string): Promise<EdgeGateState> { return this.send(appID); }
  close(appID: string, generation: string): Promise<EdgeGateState> {
    return this.send(appID, { action: "close", generation });
  }
  select(appID: string, generation: string, scriptName: string): Promise<EdgeGateState> {
    if (!scriptPattern.test(scriptName)) throw new TypeError("invalid selected API script");
    return this.send(appID, { action: "select", generation, script_name: scriptName });
  }
  open(appID: string, generation: string): Promise<EdgeGateState> {
    return this.send(appID, { action: "open", generation });
  }

  private async send(appID: string, command?: Record<string, unknown>): Promise<EdgeGateState> {
    assertResourceID("app", appID);
    if (command && (typeof command.generation !== "string" || !generationPattern.test(command.generation))) {
      throw new TypeError("invalid edge release gate generation");
    }
    let response: Response;
    try {
      response = await this.request(`${this.origin}/__cantelop/release-gates/${appID}`, {
        method: command ? "PUT" : "GET", redirect: "error",
        headers: { authorization: `Bearer ${this.token}`, ...(command ? { "content-type": "application/json" } : {}) },
        ...(command ? { body: JSON.stringify(command) } : {}),
        signal: AbortSignal.timeout(30_000),
      });
    } catch { throw new Error("edge release gate unavailable"); }
    if (response.status !== 200) throw new Error(`edge release gate returned HTTP ${response.status}`);
    const body = await response.text();
    if (Buffer.byteLength(body) > 1024) throw new Error("edge release gate response too large");
    let state: unknown;
    try { state = JSON.parse(body); }
    catch { throw new Error("edge release gate returned invalid JSON"); }
    if (!state || typeof state !== "object" || Array.isArray(state)) throw new Error("edge release gate returned invalid state");
    const value = state as Record<string, unknown>;
    if (value.app_id !== undefined && value.app_id !== appID || typeof value.closed !== "boolean" ||
        (value.generation !== null && (typeof value.generation !== "string" || !generationPattern.test(value.generation))) ||
        (value.script_name !== null && (typeof value.script_name !== "string" || !scriptPattern.test(value.script_name)))) {
      throw new Error("edge release gate returned invalid state");
    }
    return value as unknown as EdgeGateState;
  }
}

export function configuredEdgeGateProvider(environment: NodeJS.ProcessEnv = process.env): EdgeGateProvider {
  const token = environment.RELEASE_GATE_TOKEN?.trim();
  if (!token) throw new Error("RELEASE_GATE_TOKEN is required");
  return new EdgeGateProvider({ origin: environment.RELEASE_GATE_ORIGIN?.trim() ?? `https://release-gate.${configuredAppDomain(environment)}`, token });
}
