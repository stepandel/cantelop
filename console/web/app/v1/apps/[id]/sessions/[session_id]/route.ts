import { type NextRequest, NextResponse } from "next/server";

import { authenticatedUserID, authenticationErrorResponse } from "@core/http/auth";
import { productErrorResponse } from "@core/http/response";
import { getCustomerSession, terminateCustomerSession } from "@/lib/customer-operations";

export const dynamic = "force-dynamic";
const sessionIDPattern = /^ses_[A-Za-z0-9][A-Za-z0-9_-]{0,59}$/;

export async function GET(request: NextRequest, context: { params: Promise<{ id: string; session_id: string }> }) {
  return sessionRequest(request, context, "read");
}

export async function DELETE(request: NextRequest, context: { params: Promise<{ id: string; session_id: string }> }) {
  return sessionRequest(request, context, "write");
}

async function sessionRequest(
  request: NextRequest,
  context: { params: Promise<{ id: string; session_id: string }> },
  operation: "read" | "write",
) {
  let userID: string;
  try { userID = await authenticatedUserID(request.headers.get("authorization"), `tenant:${operation}`); }
  catch (error) { return authenticationErrorResponse(error); }
  try {
    const { id, session_id: sessionID } = await context.params;
    if (!sessionIDPattern.test(sessionID)) throw new TypeError("invalid Session ID");
    const session = operation === "write"
      ? await terminateCustomerSession(userID, id, sessionID)
      : await getCustomerSession(userID, id, sessionID);
    const response = NextResponse.json(session);
    response.headers.set("Cache-Control", "private, no-store");
    return response;
  } catch (error) {
    return productErrorResponse(error);
  }
}
