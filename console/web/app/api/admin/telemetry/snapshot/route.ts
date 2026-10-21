import { NextResponse } from "next/server";

import { getAdminSession } from "@/lib/admin-session";
import { getSnapshot } from "@/lib/fire-fuse";

export const dynamic = "force-dynamic";

export async function GET() {
  if (!(await getAdminSession())) return NextResponse.json({ error: "unauthorized" }, { status: 401 });
  try {
    const response = NextResponse.json(await getSnapshot());
    response.headers.set("Cache-Control", "private, no-store");
    return response;
  } catch (error) {
    console.error("admin_snapshot_unavailable", error);
    return NextResponse.json({ error: "observer_unavailable" }, { status: 502 });
  }
}
