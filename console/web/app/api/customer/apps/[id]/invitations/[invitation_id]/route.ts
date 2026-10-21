import { invitationControl } from "@core/invitation-control";
import { getUserSession } from "@/lib/user-session";
import { isSameConsoleOrigin } from "@/lib/console-origin";
import { invitationErrorResponse, invitationResponse } from "@/lib/invitation-http";

type Context = { params: Promise<{ id: string; invitation_id: string }> };
export const dynamic = "force-dynamic";

async function mutate(request: Request, context: Context, resend: boolean) {
  const session = await getUserSession();
  if (!session) return invitationResponse({ error: "unauthorized" }, 401);
  if (!isSameConsoleOrigin(request.headers)) return invitationResponse({ error: "invalid_request" }, 400);
  try {
    const { id, invitation_id } = await context.params;
    if (resend) return invitationResponse(await invitationControl().resend(session.user.id, id, invitation_id));
    await invitationControl().revoke(session.user.id, id, invitation_id);
    return invitationResponse({ revoked: true });
  } catch (error) { return invitationErrorResponse(error); }
}

export function POST(request: Request, context: Context) { return mutate(request, context, true); }
export function DELETE(request: Request, context: Context) { return mutate(request, context, false); }
