import { type NextRequest, NextResponse } from "next/server";

import { appRouteDetail } from "@core/api-routes/control";
import { routeQuery } from "@core/api-routes/query";
import { authenticatedUserID, authenticationErrorResponse } from "@core/http/auth";
import { productErrorResponse } from "@core/http/response";

export const dynamic = "force-dynamic";

// One route of the App: ?method=POST&path=/chat.
export async function GET(request: NextRequest, context: { params: Promise<{ id: string }> }) {
  let userID: string;
  try { userID = await authenticatedUserID(request.headers.get("authorization"), "tenant:read"); }
  catch (error) { return authenticationErrorResponse(error); }
  try {
    const { method, path } = routeQuery(request.nextUrl.searchParams);
    const response = NextResponse.json(await appRouteDetail(userID, (await context.params).id, method, path));
    response.headers.set("Cache-Control", "private, no-store");
    return response;
  } catch (error) {
    return productErrorResponse(error);
  }
}
