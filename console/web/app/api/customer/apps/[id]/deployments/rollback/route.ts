import { type NextRequest, NextResponse } from "next/server";
import { deploymentControl } from "@core/deployment-uploads/control";
import { productErrorResponse, unauthorizedResponse } from "@core/http/response";
import { readControlJSON } from "@core/http/request";
import { getUserSession } from "@/lib/user-session";
import { isSameConsoleOrigin } from "@/lib/console-origin";

/** Reactivate the version shown in the console, if it is still the rollback target. */
export async function POST(request: NextRequest, context: { params: Promise<{ id: string }> }) {
  const session = await getUserSession();
  if (!session) return unauthorizedResponse();
  if (!isSameConsoleOrigin(request.headers)) return NextResponse.json({ error: "invalid_request" }, { status: 400 });
  try {
    const input = await readControlJSON(request, 128);
    if (input === null || typeof input !== "object" || Array.isArray(input) ||
        Object.keys(input).length !== 1 || typeof (input as Record<string, unknown>).target_release_id !== "string") {
      throw new TypeError("invalid rollback target");
    }
    const release = await deploymentControl().rollback(session.user.id, (await context.params).id, (input as { target_release_id: string }).target_release_id);
    const response = NextResponse.json(release, { status: 202 });
    response.headers.set("Cache-Control", "private, no-store");
    return response;
  } catch (error) { return productErrorResponse(error); }
}
