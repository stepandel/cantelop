import { type NextRequest, NextResponse } from "next/server";
import { deploymentControl } from "@core/deployment-uploads/control";
import { productErrorResponse, unauthorizedResponse } from "@core/http/response";
import { getUserSession } from "@/lib/user-session";
import { isSameConsoleOrigin } from "@/lib/console-origin";

export async function POST(request: NextRequest, context: { params: Promise<{ id: string; release_id: string; action: string }> }) {
  const session = await getUserSession();
  if (!session) return unauthorizedResponse();
  if (!isSameConsoleOrigin(request.headers)) return NextResponse.json({ error: "invalid_request" }, { status: 400 });
  const userID = session.user.id;
  try {
    const { id, release_id: releaseID, action } = await context.params;
    if (action !== "cancel" && action !== "restart") return NextResponse.json({ error: "not_found" }, { status: 404 });
    const release = await deploymentControl().recover(userID, id, releaseID, action === "restart");
    const response = NextResponse.json(release, { status: release.status === "cancelling" ? 202 : 200 });
    response.headers.set("Cache-Control", "private, no-store");
    return response;
  } catch (error) { return productErrorResponse(error); }
}
