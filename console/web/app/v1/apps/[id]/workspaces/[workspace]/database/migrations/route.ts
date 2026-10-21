import { type NextRequest, NextResponse } from "next/server";
import { authenticatedUserID, authenticationErrorResponse } from "@core/http/auth";
import { productErrorResponse } from "@core/http/response";
import { productControl } from "@core/product-control";
import { DatabaseAccessError } from "@core/workspace-databases/service";
import { workspaceDatabases } from "@core/workspace-databases/configured";
export const dynamic = "force-dynamic";
export async function GET(request: NextRequest, context: { params: Promise<{ id: string; workspace: string }> }) {
  let userID: string;
  try { userID = await authenticatedUserID(request.headers.get("authorization"), "tenant:read"); }
  catch (error) { return authenticationErrorResponse(error); }
  try {
    const { id, workspace } = await context.params;
    await productControl().getApp(userID, id);
    const result = await workspaceDatabases().migrationStatus(id, workspace);
    return NextResponse.json(result, { headers: { "Cache-Control": "private, no-store" } });
  } catch (error) {
    if (error instanceof DatabaseAccessError) return NextResponse.json({ error: { code: error.code } }, { status: error.code === "workspace_not_found" ? 404 : 503, headers: { "Cache-Control": "private, no-store" } });
    return productErrorResponse(error);
  }
}
