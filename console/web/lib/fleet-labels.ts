// Words the Fleet screens share. Pure, so the client components and their
// tests read the same thing.

export function capacityLabel(used: number, cap: number | null): string {
  return `${used} / ${cap === null ? "∞" : cap}`;
}

export type PrincipalState = "active" | "expired" | "revoked";

export function principalState(
  principal: { expires_at: string; revoked_at: string | null },
  now: number,
): PrincipalState {
  if (principal.revoked_at !== null) return "revoked";
  return Date.parse(principal.expires_at) <= now ? "expired" : "active";
}

export function fleetErrorMessage(code: string | undefined): string {
  switch (code) {
    case "invalid_request":
    case "invalid_name":
    case "invalid_limit":
      return "Check the values: a name is lowercase letters, digits and hyphens starting with a letter; a cap is 1–10,000 or unlimited";
    case "invalid_principal":
      return "A principal needs a label, at least one scope, and an expiry of 1–366 days";
    case "quota_exceeded":
      return "A Fleet uses one app slot, and your account quota is full";
    case "user_suspended":
      return "This account is suspended";
    case "not_found":
      return "This Fleet or principal is no longer available";
    case "unauthorized":
      return "Your session has expired — sign in again";
    default:
      return "The control service could not complete that request";
  }
}

// Bytes in the console's lowercase voice, up to the TiB a policy allows.
export function formatStorage(bytes: number): string {
  if (bytes === 0) return "0";
  const units = ["b", "kib", "mib", "gib", "tib"];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${Number.isInteger(value) ? value : value.toFixed(1)} ${units[unit]}`;
}

// Credit amounts are bigint decimal strings; group them without Number.
export function formatCredits(value: string): string {
  const negative = value.startsWith("-");
  const digits = negative ? value.slice(1) : value;
  return `${negative ? "-" : ""}${digits.replace(/\B(?=(\d{3})+(?!\d))/g, ",")}`;
}
