import { NextResponse } from "next/server";
import { InvitationError } from "@core/db/invitation-repository";
import { productErrorResponse } from "@core/http/response";
import { isSameConsoleOrigin } from "./console-origin";
import { readBoundedUTF8Body } from "@core/http/bounded-body";

export function validInvitationMutation(request: Request): boolean {
  return isSameConsoleOrigin(request.headers) &&
    Boolean(request.headers.get("content-type")?.toLowerCase().startsWith("application/json"));
}

export async function invitationInput(request: Request): Promise<Record<string, unknown>> {
  let text: string;
  try { text = await readBoundedUTF8Body(request.body, 4096); }
  catch { throw new TypeError("invalid request"); }
  if (!text || text.length > 1024) throw new TypeError("invalid request");
  let value: unknown;
  try { value = JSON.parse(text); } catch { throw new TypeError("invalid JSON"); }
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new TypeError("invalid request");
  return value as Record<string, unknown>;
}

export function invitationResponse(body: unknown, status = 200) {
  return NextResponse.json(body, { status, headers: { "Cache-Control": "private, no-store" } });
}

export function invitationErrorResponse(error: unknown) {
  if (error instanceof InvitationError) {
    const status = { invalid_invitation: 410, email_mismatch: 403, already_invited: 409, invite_rate_limited: 429, invitation_suppressed: 409 }[error.code];
    return invitationResponse({ error: error.code }, status);
  }
  const response = productErrorResponse(error);
  response.headers.set("Cache-Control", "private, no-store");
  return response;
}
