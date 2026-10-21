import { type NextRequest, NextResponse } from "next/server";

import { isSameConsoleOrigin } from "@/lib/console-origin";
import { getUserSession } from "@/lib/user-session";
import { deleteWorkspaceResponse } from "@/lib/workspace-deletion";

export const dynamic = "force-dynamic";

export async function DELETE(request: NextRequest, context: { params: Promise<{ id: string }> }) {
  const session = await getUserSession();
  if (!session) return NextResponse.json({ error: "unauthorized" }, { status: 401 });
  if (!isSameConsoleOrigin(request.headers)) {
    return NextResponse.json({ error: "invalid_request" }, { status: 400 });
  }
  const { id } = await context.params;
  return deleteWorkspaceResponse(session.user.id, id);
}
