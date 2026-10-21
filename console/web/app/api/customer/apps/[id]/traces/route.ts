import { type NextRequest, NextResponse } from "next/server";

import { productErrorResponse } from "@core/http/response";
import { observationRepository } from "@core/observability/control";
import { productControl } from "@core/product-control";
import { getUserSession } from "@/lib/user-session";

export const dynamic = "force-dynamic";

export async function GET(
  _request: NextRequest,
  context: { params: Promise<{ id: string }> },
) {
  const session = await getUserSession();
  if (!session) return NextResponse.json({ error: "unauthorized" }, { status: 401 });
  const { id } = await context.params;
  if (!/^app_[0-9a-f]{32}$/.test(id)) {
    return NextResponse.json({ error: "not_found" }, { status: 404 });
  }
  try {
    await productControl().getApp(session.user.id, id);
    const response = NextResponse.json(await observationRepository().traceTree(id));
    response.headers.set("Cache-Control", "private, no-store");
    return response;
  } catch (error) {
    const productResponse = productErrorResponse(error);
    if (productResponse.status !== 500) return productResponse;
    return NextResponse.json({ error: "observability_unavailable" }, { status: 503 });
  }
}
