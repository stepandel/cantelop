import { type NextRequest, NextResponse } from "next/server";

import { getCustomerSnapshot } from "@/lib/customer-telemetry";
import { getUserSession } from "@/lib/user-session";

export const dynamic = "force-dynamic";

const resourceIDPattern = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/;

export async function GET(request: NextRequest) {
  const session = await getUserSession();
  if (!session) return NextResponse.json({ error: "unauthorized" }, { status: 401 });
  const incoming = request.nextUrl.searchParams;
  for (const key of incoming.keys()) {
    if ((key !== "app_id" && key !== "workspace_id") || incoming.getAll(key).length !== 1) {
      return NextResponse.json({ error: "invalid_query" }, { status: 400 });
    }
  }
  const appID = incoming.get("app_id");
  const workspaceID = incoming.get("workspace_id");
  if ((appID !== null && !resourceIDPattern.test(appID)) ||
      (workspaceID !== null && !resourceIDPattern.test(workspaceID))) {
    return NextResponse.json({ error: "invalid_query" }, { status: 400 });
  }
  try {
    const snapshot = await getCustomerSnapshot(session.user.id, {
      ...(appID === null ? {} : { appID }),
      ...(workspaceID === null ? {} : { workspaceID }),
    });
    const response = NextResponse.json(snapshot);
    response.headers.set("Cache-Control", "private, no-store");
    return response;
  } catch {
    return NextResponse.json({ error: "control_unavailable" }, { status: 502 });
  }
}
