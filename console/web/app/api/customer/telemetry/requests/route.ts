import { type NextRequest, NextResponse } from "next/server";

import { getCustomerRequestEvents } from "@/lib/customer-telemetry";
import { getUserSession } from "@/lib/user-session";

export const dynamic = "force-dynamic";

const resourceIDPattern = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/;

export async function GET(request: NextRequest) {
  const session = await getUserSession();
  if (!session) return NextResponse.json({ error: "unauthorized" }, { status: 401 });
  const incoming = request.nextUrl.searchParams;
  for (const key of incoming.keys()) {
    if ((key !== "app_id" && key !== "workspace_id" && key !== "before_sequence" && key !== "limit") || incoming.getAll(key).length !== 1) {
      return NextResponse.json({ error: "invalid_query" }, { status: 400 });
    }
  }
  const appID = incoming.get("app_id");
  const workspaceID = incoming.get("workspace_id");
  const before = incoming.get("before_sequence");
  const limit = incoming.get("limit") ?? "50";
  if ((appID === null && workspaceID === null) ||
      (appID !== null && !resourceIDPattern.test(appID)) ||
      (workspaceID !== null && !resourceIDPattern.test(workspaceID)) ||
      (before !== null && (!/^\d+$/.test(before) || Number(before) < 1)) ||
      !/^\d+$/.test(limit) || Number(limit) < 1 || Number(limit) > 500) {
    return NextResponse.json({ error: "invalid_query" }, { status: 400 });
  }
  const outgoing = new URLSearchParams({ limit });
  if (appID) outgoing.set("app_id", appID);
  if (workspaceID) outgoing.set("workspace_id", workspaceID);
  if (before) outgoing.set("before_sequence", before);
  try {
    const response = NextResponse.json(await getCustomerRequestEvents(session.user.id, outgoing));
    response.headers.set("Cache-Control", "private, no-store");
    return response;
  } catch {
    return NextResponse.json({ error: "control_unavailable" }, { status: 502 });
  }
}
