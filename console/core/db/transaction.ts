export interface TransactionQueryClient {
  query(sql: string, values?: unknown[]): Promise<unknown>;
  // node-postgres destroys, rather than reuses, a client released with an error.
  release(error?: Error | boolean): void;
}

export interface TransactionPool<Client extends TransactionQueryClient> {
  connect(): Promise<Client>;
}

export interface TransactionOptions {
  // Bounds lock waits for this transaction only (SET LOCAL lock_timeout).
  lockTimeoutMilliseconds?: number;
}

/**
 * Runs `operation` inside BEGIN/COMMIT on one pooled client. A failed ROLLBACK
 * never replaces the operation's error, and a client whose BEGIN or ROLLBACK
 * failed is discarded instead of returned to the pool in an unknown state.
 */
export async function withTransaction<Client extends TransactionQueryClient, T>(
  pool: TransactionPool<Client>,
  operation: (client: Client) => Promise<T>,
  options: TransactionOptions = {},
): Promise<T> {
  const { lockTimeoutMilliseconds } = options;
  if (lockTimeoutMilliseconds !== undefined &&
      (!Number.isSafeInteger(lockTimeoutMilliseconds) || lockTimeoutMilliseconds < 1)) {
    throw new TypeError("invalid transaction lock timeout");
  }
  const client = await pool.connect();
  let broken: Error | undefined;
  try {
    try {
      await client.query("BEGIN");
    } catch (error) {
      broken = asError(error);
      throw error;
    }
    try {
      if (lockTimeoutMilliseconds !== undefined) {
        await client.query(`SET LOCAL lock_timeout = '${lockTimeoutMilliseconds}ms'`);
      }
      const result = await operation(client);
      await client.query("COMMIT");
      return result;
    } catch (error) {
      try {
        await client.query("ROLLBACK");
      } catch (rollbackError) {
        broken = asError(rollbackError);
      }
      throw error;
    }
  } finally {
    client.release(broken);
  }
}

function asError(value: unknown): Error {
  return value instanceof Error ? value : new Error(String(value));
}
