import { type NextRequest, NextResponse } from "next/server";

import { configurationControl } from "@core/configuration/control";
import { maxIntegrationRotationRequestBytes, parseIntegrationRotationRequest } from "@core/configuration/integration-request";
import { readControlJSON } from "@core/http/request";
import { productErrorResponse, unauthorizedResponse } from "@core/http/response";
import { publicCredentialState } from "@/lib/integration-credentials";
import { isSameConsoleOrigin } from "@/lib/console-origin";
import { getUserSession } from "@/lib/user-session";

export const dynamic = "force-dynamic";

/** `cantelop app credentials rotate`: `{ grace_seconds }` or `{ retry: true }`.
 *  The new token is returned once so the page can show it for copying. */
export async function POST(request: NextRequest, context: { params: Promise<{ id: string }> }) {
  const session = await getUserSession();
  if (!session) return unauthorizedResponse();
  if (!isSameConsoleOrigin(request.headers)) return NextResponse.json({ error: "invalid_request" }, { status: 400 });
  try {
    const grace = parseIntegrationRotationRequest(await readControlJSON(request, maxIntegrationRotationRequestBytes));
    const credentials = await configurationControl().rotateIntegrationForUser(session.user.id, (await context.params).id, grace);
    return NextResponse.json({ ...publicCredentialState(credentials), token: credentials.token }, { headers: { "Cache-Control": "private, no-store" } });
  } catch (error) { return productErrorResponse(error); }
}
