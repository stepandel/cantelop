import { type NextRequest, NextResponse } from "next/server";

import { getAdminSession } from "@/lib/admin-session";
import { getInfrastructure } from "@/lib/fire-fuse";
import type { InfrastructureRange } from "@/lib/infrastructure-types";

export const dynamic = "force-dynamic";

const ranges = new Set<InfrastructureRange>(["1h", "6h", "24h"]);

export async function GET(request: NextRequest) {
  if (!(await getAdminSession())) return NextResponse.json({ error: "unauthorized" }, { status: 401 });
  const incoming = request.nextUrl.searchParams;
  for (const key of incoming.keys()) {
    if (key !== "range" || incoming.getAll(key).length !== 1) {
      return NextResponse.json({ error: "invalid_query" }, { status: 400 });
    }
  }
  const range = incoming.get("range") ?? "1h";
  if (!ranges.has(range as InfrastructureRange)) {
    return NextResponse.json({ error: "invalid_query" }, { status: 400 });
  }
  try {
    const response = NextResponse.json(await getInfrastructure(range as InfrastructureRange));
    response.headers.set("Cache-Control", "private, no-store");
    return response;
  } catch (error) {
    console.error("admin_infrastructure_unavailable", error);
    return NextResponse.json({ error: "observer_unavailable" }, { status: 502 });
  }
}
