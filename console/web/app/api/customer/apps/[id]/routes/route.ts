import { type NextRequest, NextResponse } from "next/server";

import { appRoutes } from "@core/api-routes/control";
import { productErrorResponse } from "@core/http/response";
import { getUserSession } from "@/lib/user-session";

export const dynamic = "force-dynamic";

export async function GET(_request: NextRequest, context: { params: Promise<{ id: string }> }) {
  const session = await getUserSession();
  if (!session) return NextResponse.json({ error: "unauthorized" }, { status: 401 });
  const { id } = await context.params;
  if (!/^app_[0-9a-f]{32}$/.test(id)) return NextResponse.json({ error: "invalid_request" }, { status: 400 });
  try {
    const response = NextResponse.json(await appRoutes(session.user.id, id));
    response.headers.set("Cache-Control", "private, no-store");
    return response;
  } catch (error) {
    return productErrorResponse(error);
  }
}
