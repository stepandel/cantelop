import { type NextRequest, NextResponse } from "next/server";

import { productControl } from "@core/product-control";
import { authenticatedUserID, authenticationErrorResponse } from "@core/http/auth";
import { publicCreateAppInput } from "@core/http/input";
import { readControlJSON } from "@core/http/request";
import { productErrorResponse } from "@core/http/response";

export const dynamic = "force-dynamic";

export async function GET(request: NextRequest) {
  let userID: string;
  try { userID = await authenticatedUserID(request.headers.get("authorization"), "tenant:read"); }
  catch (error) { return authenticationErrorResponse(error); }
  try {
    return noStore(await productControl().listApps(userID));
  } catch (error) {
    return productErrorResponse(error);
  }
}

export async function POST(request: NextRequest) {
  let userID: string;
  try { userID = await authenticatedUserID(request.headers.get("authorization"), "tenant:write"); }
  catch (error) { return authenticationErrorResponse(error); }
  try {
    const input = publicCreateAppInput(await readControlJSON(request, 16 * 1024));
    return noStore(await productControl().createApp(userID, input), 201);
  } catch (error) {
    return productErrorResponse(error);
  }
}

function noStore(body: unknown, status = 200) {
  const response = NextResponse.json(body, { status });
  response.headers.set("Cache-Control", "private, no-store");
  return response;
}
