import { type NextRequest, NextResponse } from "next/server";

import { deploymentControl } from "@core/deployment-uploads/control";
import { authenticatedUserID, authenticationErrorResponse } from "@core/http/auth";
import { readControlJSON } from "@core/http/request";
import { productErrorResponse } from "@core/http/response";

export const dynamic = "force-dynamic";

export async function POST(
  request: NextRequest,
  context: { params: Promise<{ id: string; upload_id: string }> },
) {
  let userID: string;
  try { userID = await authenticatedUserID(request.headers.get("authorization"), "tenant:write"); }
  catch (error) { return authenticationErrorResponse(error); }
  try {
    const input = await readControlJSON(request, 1 << 20);
    const { id, upload_id: uploadID } = await context.params;
    const release = await deploymentControl().finalizeUpload(userID, id, uploadID, input);
    const response = NextResponse.json(release, { status: release.status === "active" ? 200 : 202 });
    response.headers.set("Cache-Control", "private, no-store");
    return response;
  } catch (error) {
    return productErrorResponse(error);
  }
}
