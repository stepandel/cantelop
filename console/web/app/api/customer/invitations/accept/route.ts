import { invitationControl } from "@core/invitation-control";
import { getVerifiedUserSession } from "@/lib/verified-user-session";
import { invitationErrorResponse, invitationInput, invitationResponse, validInvitationMutation } from "@/lib/invitation-http";

export const dynamic = "force-dynamic";
export async function POST(request: Request) {
  const session = await getVerifiedUserSession();
  if (!session) return invitationResponse({ error: "unauthorized" }, 401);
  if (!validInvitationMutation(request)) return invitationResponse({ error: "invalid_request" }, 400);
  // No provider-confirmed address is not a mismatch: the provider could not
  // vouch for it right now, and signing in again is the way forward.
  if (!session.user.verifiedEmail) return invitationResponse({ error: "reauthentication_required" }, 401);
  try {
    const input = await invitationInput(request);
    if (typeof input.token !== "string") throw new TypeError("invalid token");
    const appID = await invitationControl().accept(session.user.id, session.user.verifiedEmail, input.token);
    return invitationResponse({ appID });
  } catch (error) { return invitationErrorResponse(error); }
}
