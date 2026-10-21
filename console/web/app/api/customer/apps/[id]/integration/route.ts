import { NextResponse } from "next/server";

import { configurationControl } from "@core/configuration/control";
import { ControlError } from "@core/domain/errors";
import { productErrorResponse, unauthorizedResponse } from "@core/http/response";
import { publicCredentialState } from "@/lib/integration-credentials";
import { getUserSession } from "@/lib/user-session";

export const dynamic = "force-dynamic";

/** The App's integration credential without its token: the page shows state
 *  and reveals the token only on an explicit request. */
export async function GET(_request: Request, context: { params: Promise<{ id: string }> }) {
  const session = await getUserSession();
  if (!session) return unauthorizedResponse();
  try {
    const credentials = await configurationControl().integrationCredentialsForUser(session.user.id, (await context.params).id);
    return NextResponse.json(publicCredentialState(credentials), { headers: { "Cache-Control": "private, no-store" } });
  } catch (error) {
    if (error instanceof ControlError && error.code === "integration_not_configured") {
      return NextResponse.json({ issued: false }, { headers: { "Cache-Control": "private, no-store" } });
    }
    return productErrorResponse(error);
  }
}
