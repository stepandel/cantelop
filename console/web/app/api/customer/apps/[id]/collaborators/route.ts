import { type NextRequest, NextResponse } from "next/server";

import { invitationControl } from "@core/invitation-control";
import { invitationInput, invitationErrorResponse, validInvitationMutation } from "@/lib/invitation-http";
import { getUserSession } from "@/lib/user-session";

export const dynamic = "force-dynamic";

export async function POST(request: NextRequest, context: { params: Promise<{ id: string }> }) {
  const session = await getUserSession();
  if (!session) return NextResponse.json({ error: "unauthorized" }, { status: 401 });
  if (!validInvitationMutation(request)) return NextResponse.json({ error: "invalid_request" }, { status: 400 });
  try {
    const input = await invitationInput(request);
    if (typeof input.email !== "string") throw new TypeError("invalid request");
    return noStore(await invitationControl().add(session.user.id, (await context.params).id, input.email), 201);
  } catch (error) {
    return invitationErrorResponse(error);
  }
}

function noStore(body: unknown, status = 200) {
  const response = NextResponse.json(body, { status });
  response.headers.set("Cache-Control", "private, no-store");
  return response;
}
