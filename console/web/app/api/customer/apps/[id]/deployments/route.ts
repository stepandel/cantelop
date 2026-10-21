import { NextResponse } from "next/server";

import { getCustomerReleases } from "@/lib/customer-deployments";
import { getUserSession } from "@/lib/user-session";

export const dynamic = "force-dynamic";

// The App screen polls this while a deploy is in flight, so the release list
// stays live without a page reload.
export async function GET(_request: Request, context: { params: Promise<{ id: string }> }) {
  const session = await getUserSession();
  if (!session) return NextResponse.json({ error: "unauthorized" }, { status: 401 });
  const { id } = await context.params;
  if (!/^app_[0-9a-f]{32}$/.test(id)) {
    return NextResponse.json({ error: "not_found" }, { status: 404 });
  }
  try {
    const response = NextResponse.json({ items: await getCustomerReleases(session.user.id, id) });
    response.headers.set("Cache-Control", "private, no-store");
    return response;
  } catch (error) {
    console.error("customer_releases_unavailable", error);
    return NextResponse.json({ error: "control_unavailable" }, { status: 502 });
  }
}
