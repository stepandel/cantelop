import { type NextRequest, NextResponse } from "next/server";

import { appRoutes } from "@core/api-routes/control";
import { authenticatedUserID, authenticationErrorResponse } from "@core/http/auth";
import { productErrorResponse } from "@core/http/response";
import { productControl } from "@core/product-control";

export const dynamic = "force-dynamic";

// The App's route overview, with the public origin the routes are served from
// so a client can turn it into requests or an OpenAPI document.
export async function GET(request: NextRequest, context: { params: Promise<{ id: string }> }) {
  let userID: string;
  try { userID = await authenticatedUserID(request.headers.get("authorization"), "tenant:read"); }
  catch (error) { return authenticationErrorResponse(error); }
  try {
    const appID = (await context.params).id;
    const app = await productControl().getApp(userID, appID);
    const response = NextResponse.json({ origin: `https://${app.hostname}`, ...await appRoutes(userID, appID) });
    response.headers.set("Cache-Control", "private, no-store");
    return response;
  } catch (error) {
    return productErrorResponse(error);
  }
}
