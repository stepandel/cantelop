import { type NextRequest, NextResponse } from "next/server";

import { deploymentControl } from "@core/deployment-uploads/control";
import { authenticatedUserID, authenticationErrorResponse } from "@core/http/auth";
import { productErrorResponse } from "@core/http/response";

export const dynamic = "force-dynamic";

export async function DELETE(
  request: NextRequest,
  context: { params: Promise<{ id: string; upload_id: string }> },
) {
  let userID: string;
  try { userID = await authenticatedUserID(request.headers.get("authorization"), "tenant:write"); }
  catch (error) { return authenticationErrorResponse(error); }
  try {
    const { id, upload_id: uploadID } = await context.params;
    const upload = await deploymentControl().cancelUpload(userID, id, uploadID);
    const response = NextResponse.json(upload);
    response.headers.set("Cache-Control", "private, no-store");
    return response;
  } catch (error) {
    return productErrorResponse(error);
  }
}
