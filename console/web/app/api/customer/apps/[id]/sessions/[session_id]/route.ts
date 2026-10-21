import { type NextRequest, NextResponse } from "next/server";

import { productErrorResponse, unauthorizedResponse } from "@core/http/response";
import { isSameConsoleOrigin } from "@/lib/console-origin";
import { terminateCustomerSession } from "@/lib/customer-operations";
import { getUserSession } from "@/lib/user-session";

export const dynamic = "force-dynamic";
const sessionIDPattern = /^ses_[A-Za-z0-9][A-Za-z0-9_-]{0,59}$/;

/** Stop one Session — what `session.stop()` does from the SDK. The Workspace,
 *  its files and its history stay; the next dispatch starts the Session again. */
export async function DELETE(request: NextRequest, context: { params: Promise<{ id: string; session_id: string }> }) {
  const session = await getUserSession();
  if (!session) return unauthorizedResponse();
  if (!isSameConsoleOrigin(request.headers)) return NextResponse.json({ error: "invalid_request" }, { status: 400 });
  const { id, session_id: sessionID } = await context.params;
  if (!sessionIDPattern.test(sessionID)) return NextResponse.json({ error: "not_found" }, { status: 404 });
  try {
    const stopped = await terminateCustomerSession(session.user.id, id, sessionID);
    return NextResponse.json(stopped, { headers: { "Cache-Control": "private, no-store" } });
  } catch (error) { return productErrorResponse(error); }
}
