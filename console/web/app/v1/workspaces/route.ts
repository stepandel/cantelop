import { type NextRequest, NextResponse } from "next/server";

import { productControl } from "@core/product-control";
import { authenticatedUserID, authenticationErrorResponse } from "@core/http/auth";
import { publicCreateWorkspaceInput } from "@core/http/input";
import { readControlJSON } from "@core/http/request";
import { productErrorResponse } from "@core/http/response";

export const dynamic = "force-dynamic";

export async function GET(request: NextRequest) {
  let userID: string;
  try { userID = await authenticatedUserID(request.headers.get("authorization"), "tenant:read"); }
  catch (error) { return authenticationErrorResponse(error); }
  try {
    const appID = request.nextUrl.searchParams.get("app_id") ?? undefined;
    if ([...request.nextUrl.searchParams.keys()].some((key) => key !== "app_id")) throw new TypeError("invalid query");
    const response = NextResponse.json(await productControl().listWorkspaces(userID, appID));
    response.headers.set("Cache-Control", "private, no-store");
    return response;
  } catch (error) {
    return productErrorResponse(error);
  }
}

export async function POST(request: NextRequest) {
  let userID: string;
  try { userID = await authenticatedUserID(request.headers.get("authorization"), "tenant:write"); }
  catch (error) { return authenticationErrorResponse(error); }
  try {
    const input = publicCreateWorkspaceInput(await readControlJSON(request, 4 * 1024));
    const response = NextResponse.json(await productControl().createWorkspace(userID, input), { status: 201 });
    response.headers.set("Cache-Control", "private, no-store");
    return response;
  } catch (error) {
    return productErrorResponse(error);
  }
}
