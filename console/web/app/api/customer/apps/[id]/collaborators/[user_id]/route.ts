import { type NextRequest, NextResponse } from "next/server";

import { productControl } from "@core/product-control";
import { productErrorResponse } from "@core/http/response";
import { isSameConsoleOrigin } from "@/lib/console-origin";
import { getUserSession } from "@/lib/user-session";

export async function DELETE(request: NextRequest, context: { params: Promise<{ id: string; user_id: string }> }) {
  const session = await getUserSession();
  if (!session) return NextResponse.json({ error: "unauthorized" }, { status: 401 });
  if (!isSameConsoleOrigin(request.headers)) return NextResponse.json({ error: "invalid_request" }, { status: 400 });
  const { id, user_id: collaboratorUserID } = await context.params;
  try {
    await productControl().removeAppAdmin(session.user.id, id, collaboratorUserID);
    return new NextResponse(null, { status: 204, headers: { "Cache-Control": "private, no-store" } });
  } catch (error) {
    return productErrorResponse(error);
  }
}
