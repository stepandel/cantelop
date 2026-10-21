import { createHash, createHmac, timingSafeEqual } from "node:crypto";
import { normalizeEmail } from "../domain/model";

export function recipientHash(email: string): string {
  return createHash("sha256").update(`cantelop-invitation:${normalizeEmail(email.trim())}`).digest("hex");
}
function secret(environment: NodeJS.ProcessEnv): string {
  const value = environment.CANTELOP_INVITATION_OPTOUT_SECRET;
  if (!value || value.length < 32) throw new Error("Invitation opt-out is not configured");
  return value;
}
// No email or acceptance capability in this token. It only permits stopping
// invitations. No expiry: old messages must still offer a working opt-out.
export function optOutToken(email: string, environment: NodeJS.ProcessEnv = process.env): string {
  const hash = recipientHash(email);
  const signature = createHmac("sha256", secret(environment)).update(`v1.${hash}`).digest("hex");
  return `v1.${hash}.${signature}`;
}
export function verifyOptOutToken(token: string, environment: NodeJS.ProcessEnv = process.env): string {
  const match = /^v1\.([a-f0-9]{64})\.([a-f0-9]{64})$/.exec(token);
  if (!match) throw new TypeError("Invalid preference link");
  const expected = createHmac("sha256", secret(environment)).update(`v1.${match[1]}`).digest();
  if (!timingSafeEqual(expected, Buffer.from(match[2], "hex"))) throw new TypeError("Invalid preference link");
  return match[1];
}
