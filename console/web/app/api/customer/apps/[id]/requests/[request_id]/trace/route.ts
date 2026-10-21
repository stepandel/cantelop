import { type NextRequest, NextResponse } from "next/server";

import { productErrorResponse } from "@core/http/response";
import { observationRepository } from "@core/observability/control";
import { productControl } from "@core/product-control";
import { getUserSession } from "@/lib/user-session";

export const dynamic = "force-dynamic";

// The trace of one request, polled after a request is sent from the console
// until the work it started settles.
export async function GET(_request: NextRequest, context: { params: Promise<{ id: string; request_id: string }> }) {
  const session = await getUserSession();
  if (!session) return NextResponse.json({ error: "unauthorized" }, { status: 401 });
  const { id, request_id: requestID } = await context.params;
  if (!/^app_[0-9a-f]{32}$/.test(id) || !/^req_[0-9a-f]{32}$/.test(requestID)) {
    return NextResponse.json({ error: "invalid_request" }, { status: 400 });
  }
  try {
    await productControl().getApp(session.user.id, id);
    const response = NextResponse.json(await observationRepository().requestTrace(id, requestID));
    response.headers.set("Cache-Control", "private, no-store");
    return response;
  } catch (error) {
    return productErrorResponse(error);
  }
}
