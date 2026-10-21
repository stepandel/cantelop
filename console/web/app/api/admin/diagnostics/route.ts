import { NextResponse } from "next/server";

import { getAdminSession } from "@/lib/admin-session";
import { configuredPlatformDiagnostics } from "@/lib/platform-diagnostics";

export const dynamic = "force-dynamic";

export async function GET() {
  if (!(await getAdminSession())) return NextResponse.json({ error: "unauthorized" }, { status: 401 });
  try {
    const response = NextResponse.json(await configuredPlatformDiagnostics().list());
    response.headers.set("Cache-Control", "private, no-store");
    return response;
  } catch (error) {
    console.error("diagnostics_unavailable", error);
    return NextResponse.json({ error: "diagnostics_unavailable" }, { status: 502 });
  }
}
