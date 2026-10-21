import { type NextRequest, NextResponse } from "next/server";
import { configurationControl } from "@core/configuration/control";
import {
  maxIntegrationRotationRequestBytes,
  parseIntegrationRotationRequest,
} from "@core/configuration/integration-request";
import {
  authenticatedUserID,
  authenticationErrorResponse,
} from "@core/http/auth";
import { readControlJSON } from "@core/http/request";
import { productErrorResponse } from "@core/http/response";
export const dynamic = "force-dynamic";
export async function POST(
  request: NextRequest,
  context: { params: Promise<{ id: string }> },
) {
  let userID: string;
  try {
    userID = await authenticatedUserID(
      request.headers.get("authorization"),
      "tenant:write",
    );
  } catch (error) {
    return authenticationErrorResponse(error);
  }
  try {
    const grace = parseIntegrationRotationRequest(
      await readControlJSON(request, maxIntegrationRotationRequestBytes),
    );
    const result = await configurationControl().rotateIntegrationForUser(
      userID,
      (await context.params).id,
      grace,
    );
    return NextResponse.json(result, {
      headers: { "Cache-Control": "private, no-store" },
    });
  } catch (error) {
    return productErrorResponse(error);
  }
}
