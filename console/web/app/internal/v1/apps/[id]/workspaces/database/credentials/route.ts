import { NextRequest, NextResponse } from "next/server";
import { authenticateGatewayProductControl } from "@core/http/compute-auth";
import { readControlJSON } from "@core/http/request";
import { workspaceDatabases } from "@core/workspace-databases/configured";
import { DatabaseAccessError } from "@core/workspace-databases/service";
export const dynamic = "force-dynamic";
export async function POST(request: NextRequest, context: { params: Promise<{ id: string }> }) {
  try { authenticateGatewayProductControl(request.headers.get("authorization")); }
  catch { return NextResponse.json({ error: { code: "unauthorized" } }, { status: 401, headers: { "Cache-Control": "no-store" } }); }
  try {
    const { id } = await context.params;
    const body = await readControlJSON(request, 4096);
    if (!/^app_[0-9a-f]{32}$/.test(id) || !body || typeof body !== "object" || Array.isArray(body) || Object.keys(body).join(",") !== "workspace_id" || typeof (body as { workspace_id?: unknown }).workspace_id !== "string") throw new TypeError();
    return NextResponse.json(await workspaceDatabases().applicationCredentials(id, (body as { workspace_id: string }).workspace_id), { headers: { "Cache-Control": "no-store" } });
  } catch (error) {
    const code = error instanceof DatabaseAccessError ? error.code : error instanceof TypeError ? "invalid_request" : "database_unavailable";
    return NextResponse.json({ error: { code } }, { status: code === "workspace_not_found" ? 404 : code === "invalid_request" ? 400 : 503, headers: { "Cache-Control": "no-store" } });
  }
}
