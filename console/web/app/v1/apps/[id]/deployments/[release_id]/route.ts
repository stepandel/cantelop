import { type NextRequest, NextResponse } from "next/server";

import { deploymentControl } from "@core/deployment-uploads/control";
import { authenticatedUserID, authenticationErrorResponse } from "@core/http/auth";
import { productErrorResponse } from "@core/http/response";

export const dynamic = "force-dynamic";

export async function GET(
  request: NextRequest,
  context: { params: Promise<{ id: string; release_id: string }> },
) {
  let userID: string;
  try { userID = await authenticatedUserID(request.headers.get("authorization"), "tenant:read"); }
  catch (error) { return authenticationErrorResponse(error); }
  try {
    const { id, release_id: releaseID } = await context.params;
    const response = NextResponse.json(await deploymentControl().get(userID, id, releaseID));
    response.headers.set("Cache-Control", "private, no-store");
    return response;
  } catch (error) {
    return productErrorResponse(error);
  }
}
