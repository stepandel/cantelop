import { type NextRequest, NextResponse } from "next/server";
import { deploymentControl } from "@core/deployment-uploads/control";
import { productErrorResponse } from "@core/http/response";
import { authenticatedUserID, authenticationErrorResponse } from "@core/http/auth";

export async function POST(request: NextRequest, context: { params: Promise<{ id: string; release_id: string; action: string }> }) {
  let userID: string;
  try { userID = await authenticatedUserID(request.headers.get("authorization"), "tenant:write"); }
  catch (error) { return authenticationErrorResponse(error); }
  try {
    const { id, release_id: releaseID, action } = await context.params;
    if (action !== "cancel" && action !== "restart") return NextResponse.json({ error: "not_found" }, { status: 404 });
    const release = await deploymentControl().recover(userID, id, releaseID, action === "restart");
    const response = NextResponse.json(release, { status: release.status === "cancelling" ? 202 : 200 });
    response.headers.set("Cache-Control", "private, no-store");
    return response;
  } catch (error) { return productErrorResponse(error); }
}
