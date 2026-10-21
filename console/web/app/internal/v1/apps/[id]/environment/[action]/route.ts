import { type NextRequest, NextResponse } from "next/server";
import { configurationControl } from "@core/configuration/control";
import { parseEnvironmentRequest } from "@core/configuration/environment-request";
import { maxConfigMutationRequestBytes } from "@core/configuration/limits";
import { authenticateGatewayProductControl } from "@core/http/compute-auth";
import { readControlJSON } from "@core/http/request";
import { productErrorResponse, unauthorizedResponse } from "@core/http/response";

export const dynamic = "force-dynamic";

export async function POST(request: NextRequest, context: { params: Promise<{ id: string; action: string }> }) {
  try { authenticateGatewayProductControl(request.headers.get("authorization")); }
  catch { return unauthorizedResponse(); }
  try {
    const { id, action } = await context.params;
    const input = parseEnvironmentRequest(action, await readControlJSON(request, maxConfigMutationRequestBytes + 1024));
    const configuration = configurationControl();
    const update = input.type === "app.environment.status"
      ? await configuration.environmentStatus(id, input.revision)
      : input.type === "app.environment.set"
        ? await configuration.setForApp(id, input.secret ? "secret" : "variable", input.name, input.value)
        : await configuration.deleteForApp(id, input.secret ? "secret" : "variable", input.name);
    const response = NextResponse.json(update);
    response.headers.set("Cache-Control", "private, no-store");
    return response;
  } catch (error) { return productErrorResponse(error); }
}
