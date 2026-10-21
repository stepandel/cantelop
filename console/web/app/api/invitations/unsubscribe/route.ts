import { InvitationRepository } from "@core/db/invitation-repository";
import { controlDatabase } from "@core/db/database";
import { verifyOptOutToken } from "@core/email/preferences";
import { readBoundedUTF8Body } from "@core/http/bounded-body";
import { invitationInput, invitationResponse, validInvitationMutation } from "@/lib/invitation-http";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";
// GET never mutates preferences: link scanners must not unsubscribe people.
export async function POST(request: Request) {
  let hash: string;
  try {
    const url = new URL(request.url);
    const token = url.searchParams.get("token");
    if (token) {
      // RFC 8058 callbacks intentionally have no browser session/origin.
      const body = await readBoundedUTF8Body(request.body, 256);
      if (body.length > 256 || new URLSearchParams(body).get("List-Unsubscribe") !== "One-Click") throw new TypeError();
      hash = verifyOptOutToken(token);
    } else {
      if (!validInvitationMutation(request)) return invitationResponse({ error: "invalid_request" }, 400);
      const input = await invitationInput(request);
      if (typeof input.token !== "string") throw new TypeError();
      hash = verifyOptOutToken(input.token);
    }
  } catch { return invitationResponse({ error: "invalid_preference_link" }, 400); }
  await new InvitationRepository(controlDatabase()).suppress(hash);
  return invitationResponse({ unsubscribed: true });
}
