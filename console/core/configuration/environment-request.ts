import { validConfigValue, writableConfigName } from "./limits";

export type EnvironmentRequest =
  | { type: "app.environment.set"; name: string; value: string; secret: boolean }
  | { type: "app.environment.unset"; name: string; secret: boolean }
  | { type: "app.environment.status"; revision: number };

export function parseEnvironmentRequest(action: string, value: unknown): EnvironmentRequest {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new TypeError("invalid environment request");
  const input = value as Record<string, unknown>;
  if (!["set", "unset", "status"].includes(action) || input.type !== `app.environment.${action}`) throw new TypeError("invalid environment action");
  const keys = action === "set" ? ["type", "name", "value", "secret"] : action === "unset" ? ["type", "name", "secret"] : ["type", "revision"];
  if (Object.keys(input).length !== keys.length || Object.keys(input).some(key => !keys.includes(key))) throw new TypeError("invalid environment fields");
  if (action === "status") {
    if (!Number.isSafeInteger(input.revision) || (input.revision as number) < 0) throw new TypeError("invalid environment revision");
  } else {
    if (!writableConfigName(input.name) || typeof input.secret !== "boolean") throw new TypeError("invalid environment name or kind");
    if (action === "set" && !validConfigValue(input.value)) throw new TypeError("invalid environment value");
  }
  return input as unknown as EnvironmentRequest;
}
