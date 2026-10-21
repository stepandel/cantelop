import { type NextRequest, NextResponse } from "next/server";

import { relayToApp } from "@core/api-routes/control";
import { RelayError, relayLimits } from "@core/api-routes/relay";
import { readControlJSON } from "@core/http/request";
import { productErrorResponse } from "@core/http/response";
import { isSameConsoleOrigin } from "@/lib/console-origin";
import { getUserSession } from "@/lib/user-session";

export const dynamic = "force-dynamic";

// Sends a request to the App from the console. A browser write: session and
// same-origin, like the other mutating customer routes, since it makes the
// App do work under the operator's name.
export async function POST(request: NextRequest, context: { params: Promise<{ id: string }> }) {
  const session = await getUserSession();
  if (!session) return NextResponse.json({ error: "unauthorized" }, { status: 401 });
  if (!isSameConsoleOrigin(request.headers)) return NextResponse.json({ error: "invalid_request" }, { status: 400 });
  const { id } = await context.params;
  if (!/^app_[0-9a-f]{32}$/.test(id)) return NextResponse.json({ error: "invalid_request" }, { status: 400 });
  try {
    // The envelope carries the body as a string, so allow the body limit plus room for the rest.
    const input = await readControlJSON(request, relayLimits.bodyBytes * 2 + 16 * 1024);
    const response = NextResponse.json(await relayToApp(session.user.id, id, input));
    response.headers.set("Cache-Control", "private, no-store");
    return response;
  } catch (error) {
    if (error instanceof RelayError) {
      return NextResponse.json({ error: `app_${error.reason}` }, { status: 502, headers: { "Cache-Control": "private, no-store" } });
    }
    return productErrorResponse(error);
  }
}
