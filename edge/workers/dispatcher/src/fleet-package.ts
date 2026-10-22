export interface FleetPackageCode {
  digest: string;
  module: string;
  compatibility_date: string;
}

export function validPackageCode(value: unknown): value is FleetPackageCode {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  const v = value as Record<string, unknown>;
  return Object.keys(v).sort().join(",") === "compatibility_date,digest,module" &&
    typeof v.digest === "string" && /^sha256:[0-9a-f]{64}$/.test(v.digest) &&
    typeof v.module === "string" && new TextEncoder().encode(v.module).length > 0 &&
    new TextEncoder().encode(v.module).length <= 1 << 20 &&
    typeof v.compatibility_date === "string" && /^\d{4}-\d{2}-\d{2}$/.test(v.compatibility_date);
}

/** Immutable package storage. Installation deletion has no access to this object. */
export class FleetPackage {
  constructor(private readonly state: DurableObjectState) {}

  async fetch(request: Request): Promise<Response> {
    if (request.method === "GET") {
      const meta = await this.state.storage.get<{ digest: string; compatibility_date: string; chunks: number }>("meta");
      if (!meta) return new Response(null, { status: 404 });
      const keys = Array.from({ length: meta.chunks }, (_, i) => `code:${i}`);
      const chunks = await this.state.storage.get<string>(keys);
      if (chunks.size !== keys.length) return new Response(null, { status: 503 });
      return Response.json({ digest: meta.digest, compatibility_date: meta.compatibility_date,
        module: keys.map(key => chunks.get(key)!).join("") });
    }
    if (request.method !== "PUT") return new Response(null, { status: 405 });
    const code: unknown = await request.json();
    if (!validPackageCode(code)) return new Response(null, { status: 400 });
    const hash = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(code.module));
    const digest = `sha256:${Array.from(new Uint8Array(hash), b => b.toString(16).padStart(2, "0")).join("")}`;
    if (digest !== code.digest) return new Response(null, { status: 400 });
    return this.state.storage.transaction(async tx => {
      const old = await tx.get<{ digest: string; compatibility_date: string }>("meta");
      if (old) return Response.json({ digest: old.digest }, {
        status: old.digest === digest && old.compatibility_date === code.compatibility_date ? 200 : 409,
      });
      // UTF-16 chunks stay below the per-value storage limit even for non-ASCII code.
      const chunks = Math.ceil(code.module.length / 16_384);
      const values: Record<string, unknown> = {};
      for (let i = 0; i < chunks; i++) values[`code:${i}`] = code.module.slice(i * 16_384, (i + 1) * 16_384);
      values.meta = { digest, compatibility_date: code.compatibility_date, chunks };
      await tx.put(values);
      return Response.json({ digest });
    });
  }
}
