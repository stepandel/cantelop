import { defaultIntegrationGraceSeconds, maxIntegrationGraceSeconds } from "./repository";

export const maxIntegrationRotationRequestBytes = 1024;

/** `{}` keeps the previous token for the default grace period; 0 revokes it immediately. */
export function parseIntegrationRotationRequest(document: unknown): number | "retry" {
  if (typeof document !== "object" || document === null || Array.isArray(document)) throw new TypeError("invalid rotation request");
  const keys = Object.keys(document);
  if (keys.length === 0) return defaultIntegrationGraceSeconds;
  if (keys.join(",") === "retry" && (document as { retry?: unknown }).retry === true) return "retry";
  const grace = (document as { grace_seconds?: unknown }).grace_seconds;
  if (keys.join(",") !== "grace_seconds" || typeof grace !== "number" || !Number.isSafeInteger(grace) ||
      grace < 0 || grace > maxIntegrationGraceSeconds) throw new TypeError("invalid rotation request");
  return grace;
}
