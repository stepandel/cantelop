import { NextResponse } from "next/server";
import { controlDatabase } from "@core/db/database";
import { productControl } from "@core/product-control";
import { DeploymentLogRepository } from "@core/db/deployment-log-repository";
import { ControlError } from "@core/domain/errors";
import { getUserSession } from "@/lib/user-session";

export const dynamic = "force-dynamic";

export async function GET(_request: Request, context: { params: Promise<{ id: string; release_id: string }> }) {
  const headers = { "Cache-Control": "private, no-store" };
  const session = await getUserSession();
  if (!session) return NextResponse.json({ error: "unauthorized" }, { status: 401, headers });
  const { id, release_id } = await context.params;
  if (!/^app_[0-9a-f]{32}$/.test(id) || !/^rel_[0-9a-f]{32}$/.test(release_id)) {
    return NextResponse.json({ error: "not_found" }, { status: 404, headers });
  }
  try {
    const ownerUserID = await productControl().resolveAppOwner(session.user.id, id);
    return NextResponse.json(await new DeploymentLogRepository(controlDatabase()).listForUser(ownerUserID, id, release_id), { headers });
  } catch (error) {
    const missing = error instanceof ControlError && error.code === "not_found";
    return NextResponse.json({ error: missing ? "not_found" : "control_unavailable" }, { status: missing ? 404 : 502, headers });
  }
}
