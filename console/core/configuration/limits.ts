export const maxConfigValueBytes = 64 * 1024;

const configNamePattern = /^[A-Za-z_][A-Za-z0-9_]{0,127}$/;

/** A name users and Apps may write; CANTELOP_ names belong to the platform. */
export function writableConfigName(name: unknown): name is string {
  return typeof name === "string" && configNamePattern.test(name) && !name.startsWith("CANTELOP_");
}

export function validConfigValue(value: unknown): value is string {
  return typeof value === "string" && !value.includes("\0") && Buffer.byteLength(value) <= maxConfigValueBytes;
}

// A valid value can require a six-byte JSON escape for every decoded byte.
// Include the fixed {"value":""} envelope plus a small structural margin.
export const maxConfigMutationRequestBytes = maxConfigValueBytes * 6 + 32;
