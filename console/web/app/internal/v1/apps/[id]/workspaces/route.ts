import { type NextRequest, NextResponse } from "next/server";

import { productControl } from "@core/product-control";
import { authenticateGatewayProductControl } from "@core/http/compute-auth";
import { readControlJSON } from "@core/http/request";
import { productErrorResponse, unauthorizedResponse } from "@core/http/response";
import { validateCreateWorkspace } from "@core/domain/model";

export const dynamic = "force-dynamic";

export async function POST(
  request: NextRequest,
  context: { params: Promise<{ id: string }> },
) {
  try { authenticateGatewayProductControl(request.headers.get("authorization")); }
  catch { return unauthorizedResponse(); }
  try {
    const body = await readControlJSON(request, 4 * 1024);
    if (typeof body !== "object" || body === null || Array.isArray(body) ||
        Object.keys(body).length !== 1 || typeof (body as { slug?: unknown }).slug !== "string") {
      throw new TypeError("invalid Workspace input");
    }
    const { id } = await context.params;
    const input = validateCreateWorkspace({
      appID: id,
      slug: (body as { slug: string }).slug,
    });
    const response = NextResponse.json(await productControl().createWorkspaceForApp(input), { status: 201 });
    response.headers.set("Cache-Control", "private, no-store");
    return response;
  } catch (error) {
    return productErrorResponse(error);
  }
}
