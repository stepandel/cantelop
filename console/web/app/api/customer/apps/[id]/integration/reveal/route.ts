import { type NextRequest, NextResponse } from "next/server";

import { configurationControl } from "@core/configuration/control";
import { productErrorResponse, unauthorizedResponse } from "@core/http/response";
import { isSameConsoleOrigin } from "@/lib/console-origin";
import { getUserSession } from "@/lib/user-session";

export const dynamic = "force-dynamic";

/** The current token — the same read as `cantelop app credentials`. A POST, so
 *  it is never prefetched, cached or replayed by a link. */
export async function POST(request: NextRequest, context: { params: Promise<{ id: string }> }) {
  const session = await getUserSession();
  if (!session) return unauthorizedResponse();
  if (!isSameConsoleOrigin(request.headers)) return NextResponse.json({ error: "invalid_request" }, { status: 400 });
  try {
    const credentials = await configurationControl().integrationCredentialsForUser(session.user.id, (await context.params).id);
    return NextResponse.json({ token: credentials.token }, { headers: { "Cache-Control": "private, no-store" } });
  } catch (error) { return productErrorResponse(error); }
}
