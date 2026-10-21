export interface WorkspaceDatabaseProvider {
  ensure(name: string, limits?: {sizeLimit:number}): Promise<{ id: string; url: string }>;
  token(name: string): Promise<string>;
  delete(name: string): Promise<void>;
  probe(url: string, token: string): Promise<void>;
  /** Names of every database in the configured group. */
  list(): Promise<string[]>;
}
export class DatabaseProviderError extends Error {
  constructor(readonly retryable: boolean, readonly status?: number) { super("workspace database provider unavailable"); }
}
/** Only the control plane holds platformToken; workloads receive database tokens. */
export class TursoProvider implements WorkspaceDatabaseProvider {
  private readonly origin: string;
  constructor(private readonly organization: string, private readonly group: string,
    private readonly platformToken: string, private readonly request: typeof fetch = fetch) {
    if (![organization, group].every(v => /^[a-z0-9][a-z0-9-]{0,62}$/.test(v)) || !platformToken.trim()) throw new TypeError("invalid Turso configuration");
    this.origin = `https://api.turso.tech/v1/organizations/${organization}/databases`;
  }
  async ensure(name: string, limits?: {sizeLimit:number}): Promise<{ id: string; url: string }> {
    validateName(name);
    let response = await this.call("POST", "", { name, group: this.group, ...(limits?{size_limit:String(limits.sizeLimit)}:{}) });
    const existing = response.status === 409;
    if (existing) {
      response = await this.call("GET", `/${name}`);
      if (response.status === 404) throw new DatabaseProviderError(true, 404);
    }
    check(response);
    const document = await readDocument(response);
    const db = document.database;
    if (!record(db) || db.Name !== name || typeof db.DbId !== "string" || !db.DbId ||
        typeof db.Hostname !== "string" || !/^[a-z0-9.-]+\.turso\.io$/.test(db.Hostname)) throw new DatabaseProviderError(true);
    if ((existing || db.group !== undefined) && db.group !== this.group) throw new DatabaseProviderError(false);
    if(limits) {const limited=await this.call("PATCH",`/${name}/configuration`,{size_limit:String(limits.sizeLimit)});check(limited);const value=await readDocument(limited);if(Number(value.size_limit)!==limits.sizeLimit)throw new DatabaseProviderError(true);}
    return { id: db.DbId, url: `libsql://${db.Hostname}` };
  }
  async token(name: string): Promise<string> {
    validateName(name);
    const response = await this.call("POST", `/${name}/auth/tokens?expiration=15m&authorization=full-access`);
    check(response);
    const document = await readDocument(response);
    if (typeof document.jwt !== "string" || document.jwt.length < 20 || document.jwt.length > 16384) throw new DatabaseProviderError(true);
    return document.jwt;
  }
  async probe(url: string, token: string): Promise<void> {
    const endpoint = new URL(url);
    if (endpoint.protocol !== "libsql:" || !/^[a-z0-9.-]+\.turso\.io$/.test(endpoint.hostname) || endpoint.port || endpoint.username || endpoint.password) throw new DatabaseProviderError(false);
    try {
      const response = await this.request(`https://${endpoint.hostname}/v2/pipeline`, {
        method: "POST", redirect: "error", signal: AbortSignal.timeout(10_000),
        headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
        body: JSON.stringify({ requests: [{ type: "execute", stmt: { sql: "SELECT 1", want_rows: true } }, { type: "close" }] }),
      });
      check(response);
      const document = await readDocument(response);
      if (!Array.isArray(document.results) || !record(document.results[0]) || document.results[0].type !== "ok") throw new DatabaseProviderError(true);
    } catch { throw new DatabaseProviderError(true); }
  }
  async delete(name: string): Promise<void> {
    validateName(name);
    const response = await this.call("DELETE", `/${name}`);
    if (response.status !== 404) check(response);
  }
  async list(): Promise<string[]> {
    const names: string[] = [];
    let cursor: string | undefined;
    // Bound the walk so a provider that keeps returning a cursor cannot hang an operator request.
    for (let page = 0; page < 50; page++) {
      const query = new URLSearchParams({ group: this.group });
      if (cursor) query.set("cursor", cursor);
      const response = await this.call("GET", `?${query}`);
      check(response);
      const document = await readDocument(response, 4 * 1024 * 1024);
      if (!Array.isArray(document.databases)) throw new DatabaseProviderError(true);
      for (const db of document.databases) {
        if (!record(db) || typeof db.Name !== "string") throw new DatabaseProviderError(true);
        // Older API versions ignore the group filter, so apply it here as well.
        if (db.group === this.group) names.push(db.Name);
      }
      const next = record(document.pagination) ? document.pagination.next : undefined;
      if (typeof next !== "string" || !next) return names;
      cursor = next;
    }
    throw new DatabaseProviderError(true);
  }
  private async call(method: string, suffix: string, body?: unknown): Promise<Response> {
    try {
      return await this.request(this.origin + suffix, {
        method, redirect: "error", signal: AbortSignal.timeout(20_000),
        headers: { Authorization: `Bearer ${this.platformToken}`, ...(body ? { "Content-Type": "application/json" } : {}) },
        ...(body ? { body: JSON.stringify(body) } : {}),
      });
    } catch { throw new DatabaseProviderError(true); }
  }
}
function check(response: Response): void {
  if (!response.ok) throw new DatabaseProviderError(response.status === 429 || response.status >= 500, response.status);
}
async function readDocument(response: Response, limit = 65536): Promise<Record<string, unknown>> {
  try {
    const text = await response.text();
    if (text.length > limit) throw new Error();
    const document = JSON.parse(text);
    if (!record(document)) throw new Error();
    return document;
  } catch { throw new DatabaseProviderError(true); }
}
function record(value: unknown): value is Record<string, unknown> { return typeof value === "object" && value !== null && !Array.isArray(value); }
function validateName(name: string): void {
  if (!/^[a-z0-9][a-z0-9-]{0,62}$/.test(name)) throw new TypeError("invalid database name");
}
export function databaseName(environment: string, workspaceID: string): string {
  if (!/^[a-z0-9]{1,16}$/.test(environment) || !/^wsp_[0-9a-f]{32}$/.test(workspaceID)) throw new TypeError("invalid Workspace database identity");
  return `ct-${environment}-${workspaceID.slice(4)}`;
}
export function configuredTursoProvider(environment: NodeJS.ProcessEnv = process.env): TursoProvider {
  return new TursoProvider(required(environment, "CANTELOP_TURSO_ORGANIZATION"), required(environment, "CANTELOP_TURSO_GROUP"), required(environment, "CANTELOP_TURSO_PLATFORM_TOKEN"));
}
function required(environment: NodeJS.ProcessEnv, name: string): string {
  const value = environment[name]?.trim(); if (!value) throw new Error(`${name} is required`); return value;
}
