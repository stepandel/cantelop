import { Pool, type PoolConfig } from "pg";
import { drizzle } from "drizzle-orm/node-postgres";

import * as schema from "./schema";

export { withTransaction, type TransactionOptions } from "./transaction";

export interface QueryResult<Row> {
  rows: Row[];
  rowCount: number | null;
}

export interface SQLClient {
  query<Row extends Record<string, unknown> = Record<string, unknown>>(sql: string, values?: unknown[]): Promise<QueryResult<Row>>;
}

export interface TransactionClient extends SQLClient {
  release(error?: Error | boolean): void;
}

export interface SQLPool extends SQLClient {
  connect(): Promise<TransactionClient>;
}

type CompilableQuery = PromiseLike<Record<string, unknown>[]> & {
  toSQL(): { sql: string; params: unknown[] };
};

type QueryRow<Query extends CompilableQuery> = Awaited<Query>[number];

export function controlQueryBuilder(client: SQLClient) {
  return drizzle(client as never, { schema });
}

export async function executeDrizzle<Query extends CompilableQuery>(
  client: SQLClient,
  query: Query,
): Promise<QueryResult<QueryRow<Query>>> {
  const compiled = query.toSQL();
  return client.query<QueryRow<Query>>(compiled.sql, compiled.params);
}

let sharedPool: Pool | undefined;

export function controlDatabaseConfig(environment: NodeJS.ProcessEnv = process.env): PoolConfig {
  const rawURL = environment.CANTELOP_DATABASE_URL?.trim();
  if (!rawURL) throw new Error("CANTELOP_DATABASE_URL is required");
  const url = new URL(rawURL);
  if ((url.protocol !== "postgres:" && url.protocol !== "postgresql:") || !url.hostname ||
      !url.username || url.hash) {
    throw new Error("CANTELOP_DATABASE_URL must be a PostgreSQL connection URL");
  }
  const sslMode = environment.CANTELOP_DATABASE_SSL ?? "verify-full";
  if (sslMode !== "verify-full" && sslMode !== "disable") {
    throw new Error("CANTELOP_DATABASE_SSL must be verify-full or disable");
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
    application_name: "cantelop-control",
  };
}

export function controlDatabase(): Pool {
  if (!sharedPool) sharedPool = new Pool(controlDatabaseConfig());
  return sharedPool;
}

export async function closeControlDatabase(): Promise<void> {
  const pool = sharedPool;
  sharedPool = undefined;
  if (pool) await pool.end();
}
