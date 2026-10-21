import { type NextRequest, NextResponse } from "next/server";

import { productControl } from "@core/product-control";
import { authenticatedUserID, authenticationErrorResponse } from "@core/http/auth";
import { productErrorResponse } from "@core/http/response";

export const dynamic = "force-dynamic";

export async function GET(request: NextRequest, context: { params: Promise<{ id: string }> }) {
  let userID: string;
  try { userID = await authenticatedUserID(request.headers.get("authorization"), "tenant:read"); }
  catch (error) { return authenticationErrorResponse(error); }
  try {
    const response = NextResponse.json(await productControl().getApp(userID, (await context.params).id));
    response.headers.set("Cache-Control", "private, no-store");
    return response;
  } catch (error) {
    return productErrorResponse(error);
  }
}

export async function DELETE(request: NextRequest, context: { params: Promise<{ id: string }> }) {
  let userID: string;
  try { userID = await authenticatedUserID(request.headers.get("authorization"), "tenant:write"); }
  catch (error) { return authenticationErrorResponse(error); }
  try {
    await productControl().archiveApp(userID, (await context.params).id);
    return new NextResponse(null, { status: 202, headers: { "Cache-Control": "private, no-store" } });
  } catch (error) {
    return productErrorResponse(error);
  }
}
