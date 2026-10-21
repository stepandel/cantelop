import { type NextRequest, NextResponse } from "next/server";

import { productControl } from "@core/product-control";
import { productErrorResponse } from "@core/http/response";
import { isSameConsoleOrigin } from "@/lib/console-origin";
import { getUserSession } from "@/lib/user-session";

export const dynamic = "force-dynamic";

export async function DELETE(request: NextRequest, context: { params: Promise<{ id: string }> }) {
  const session = await getUserSession();
  if (!session) return NextResponse.json({ error: "unauthorized" }, { status: 401 });
  if (!isSameConsoleOrigin(request.headers)) {
    return NextResponse.json({ error: "invalid_request" }, { status: 400 });
  }
  const { id } = await context.params;
  if (!/^app_[0-9a-f]{32}$/.test(id)) {
    return NextResponse.json({ error: "not_found" }, { status: 404 });
  }
  try {
    await productControl().archiveApp(session.user.id, id);
    return new NextResponse(null, { status: 202 });
  } catch (error) {
    return productErrorResponse(error);
  }
}
