import { type NextRequest, NextResponse } from "next/server";

import { observationRepository } from "@core/observability/control";
import { getAdminSession } from "@/lib/admin-session";

export const dynamic = "force-dynamic";

export async function GET(request: NextRequest) {
  if (!(await getAdminSession())) return NextResponse.json({ error: "unauthorized" }, { status: 401 });
  const appID = request.nextUrl.searchParams.get("app_id") ?? "";
  if (!/^app_[0-9a-f]{32}$/.test(appID)) {
    return NextResponse.json({ error: "invalid_app_id" }, { status: 400 });
  }
  try {
    const response = NextResponse.json(await observationRepository().traceTree(appID));
    response.headers.set("Cache-Control", "private, no-store");
    return response;
  } catch (error) {
    console.error("admin_trace_tree_unavailable", error);
    return NextResponse.json({ error: "observability_unavailable" }, { status: 503 });
  }
}
