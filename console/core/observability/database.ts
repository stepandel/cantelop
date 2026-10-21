import { Pool, type PoolConfig } from "pg";

let sharedPool: Pool | undefined;
export const DEFAULT_OBSERVABILITY_RETENTION_HOURS = 72;

export function observabilityRetentionHours(environment: NodeJS.ProcessEnv = process.env): number {
  const raw = environment.CANTELOP_OBSERVABILITY_RETENTION_HOURS?.trim();
  if (!raw) return DEFAULT_OBSERVABILITY_RETENTION_HOURS;
  if (!/^\d+$/.test(raw)) throw new Error("CANTELOP_OBSERVABILITY_RETENTION_HOURS must be an integer from 24 to 720");
  const hours = Number(raw);
  if (!Number.isSafeInteger(hours) || hours < 24 || hours > 720) {
    throw new Error("CANTELOP_OBSERVABILITY_RETENTION_HOURS must be an integer from 24 to 720");
  }
  return hours;
}

export function observabilityDatabaseConfig(environment: NodeJS.ProcessEnv = process.env): PoolConfig {
  const rawURL = environment.CANTELOP_OBSERVABILITY_DATABASE_URL?.trim() ||
    environment.CANTELOP_DATABASE_URL?.trim();
  if (!rawURL) {
    throw new Error("CANTELOP_DATABASE_URL or CANTELOP_OBSERVABILITY_DATABASE_URL is required");
  }
  const url = new URL(rawURL);
  if ((url.protocol !== "postgres:" && url.protocol !== "postgresql:") || !url.hostname ||
      !url.username || url.hash) {
    throw new Error("the observability database URL must be a PostgreSQL connection URL");
  }
  const sslMode = environment.CANTELOP_OBSERVABILITY_DATABASE_SSL ??
    environment.CANTELOP_DATABASE_SSL ?? "verify-full";
  if (sslMode !== "verify-full" && sslMode !== "disable") {
    throw new Error("CANTELOP_OBSERVABILITY_DATABASE_SSL must be verify-full or disable");
  }
  if (sslMode === "disable" && !["localhost", "127.0.0.1", "::1"].includes(url.hostname)) {
    throw new Error("unencrypted PostgreSQL is allowed only on loopback");
  }
  url.searchParams.set("sslmode", sslMode);
  return {
    connectionString: url.toString(),
    ssl: sslMode === "verify-full" ? { rejectUnauthorized: true } : false,
    max: 10,
    connectionTimeoutMillis: 5_000,
    idleTimeoutMillis: 30_000,
    // Long migration lock waits leave the socket idle; keepalive stops
    // intermediaries with idle timeouts from silently dropping it.
    keepAlive: true,
    application_name: "cantelop-observability",
  };
}

export function observabilityDatabase(): Pool {
  if (!sharedPool) sharedPool = new Pool(observabilityDatabaseConfig());
  return sharedPool;
}

export async function closeObservabilityDatabase(): Promise<void> {
  const pool = sharedPool;
  sharedPool = undefined;
  if (pool) await pool.end();
}
