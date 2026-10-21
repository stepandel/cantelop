import { type NextRequest, NextResponse } from "next/server";
import { authenticateGatewayProductControl } from "@core/http/compute-auth";
import { assertResourceID } from "@core/domain/ids";
import { productErrorResponse, unauthorizedResponse } from "@core/http/response";
import { productControl } from "@core/product-control";

export const dynamic = "force-dynamic";

export async function DELETE(request: NextRequest, context: { params: Promise<{ id: string; workspace: string }> }) {
  try { authenticateGatewayProductControl(request.headers.get("authorization")); }
  catch { return unauthorizedResponse(); }
  try {
    const { id, workspace } = await context.params;
    assertResourceID("app", id);
    assertResourceID("workspace", workspace);
    const hostArchived = await productControl().deleteWorkspaceForApp(id, workspace);
    return NextResponse.json({ id: workspace, status: "deleting", hostArchived }, {
      status: 202, headers: { "Cache-Control": "private, no-store" },
    });
  } catch (error) { return productErrorResponse(error); }
}
