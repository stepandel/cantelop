import type { SQLClient } from "./database";

export async function allocateReleaseGeneration(client: SQLClient): Promise<string> {
  const sequence = await client.query<{ generation: string }>(`SELECT nextval('cantelop_control.release_activation_generation_seq')::text AS generation`);
  if (sequence.rowCount !== 1) throw new Error("allocate release generation returned an invalid row count");
  const value = BigInt(sequence.rows[0].generation);
  if (value < BigInt(1) || value > (BigInt(1) << BigInt(128)) - BigInt(1)) {
    throw new Error("release generation exhausted");
  }
  return `act_${value.toString(16).padStart(32, "0")}`;
}
