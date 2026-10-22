import { scopes, type FleetScope } from "@core/fleets/scopes";

export const fleetNamePattern = /^[a-z][a-z0-9-]{0,62}$/;
export const maxFleetApps = 10_000;
export const maxPrincipalDays = 366;

// Console bodies are strict: an unknown key is a client bug, not an option.
function record(input: unknown, allowed: readonly string[], what: string): Record<string, unknown> {
  if (typeof input !== "object" || input === null || Array.isArray(input) ||
      Object.keys(input).some((key) => !allowed.includes(key))) {
    throw new Error(`invalid ${what}`);
  }
  return input as Record<string, unknown>;
}

/** `null` is an explicit unlimited cap; a missing cap is refused, as the API does. */
function capacity(value: unknown): number | null {
  if (value === null) return null;
  if (!Number.isSafeInteger(value) || (value as number) < 1 || (value as number) > maxFleetApps) {
    throw new Error("invalid Fleet capacity");
  }
  return value as number;
}

export function fleetCreateSpec(input: unknown): { name: string; maxApps: number | null } {
  const parsed = record(input, ["name", "max_apps"], "Fleet settings");
  if (typeof parsed.name !== "string" || !fleetNamePattern.test(parsed.name) || !("max_apps" in parsed)) {
    throw new Error("invalid Fleet settings");
  }
  return { name: parsed.name, maxApps: capacity(parsed.max_apps) };
}

export function fleetCapacitySpec(input: unknown): { maxApps: number | null } {
  const parsed = record(input, ["max_apps"], "Fleet capacity");
  if (!("max_apps" in parsed)) throw new Error("invalid Fleet capacity");
  return { maxApps: capacity(parsed.max_apps) };
}

export function principalSpec(
  input: unknown,
  now = Date.now(),
): { label: string; scopes: FleetScope[]; expiresAt: string } {
  const parsed = record(input, ["label", "scopes", "expires_in_days"], "principal");
  const label = typeof parsed.label === "string" ? parsed.label.trim() : "";
  const chosen = parsed.scopes;
  const days = parsed.expires_in_days;
  if (!label || label.length > 128 ||
      !Array.isArray(chosen) || chosen.length === 0 || new Set(chosen).size !== chosen.length ||
      chosen.some((scope) => !(scopes as readonly unknown[]).includes(scope)) ||
      !Number.isSafeInteger(days) || (days as number) < 1 || (days as number) > maxPrincipalDays) {
    throw new Error("invalid principal");
  }
  return {
    label,
    scopes: chosen as FleetScope[],
    expiresAt: new Date(now + (days as number) * 86_400_000).toISOString(),
  };
}
