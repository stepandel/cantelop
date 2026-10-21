import { type NextRequest, NextResponse } from "next/server";
import { configurationControl } from "@core/configuration/control";
import {
  authenticatedUserID,
  authenticationErrorResponse,
} from "@core/http/auth";
import { productErrorResponse } from "@core/http/response";
export const dynamic = "force-dynamic";
type Context = { params: Promise<{ id: string }> };
/** Read the App's credentials without creating them; deployment creates them. */
export async function GET(request: NextRequest, context: Context) {
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
    const result = await configurationControl().integrationCredentialsForUser(
      userID,
      (await context.params).id,
    );
    return NextResponse.json(result, {
      headers: { "Cache-Control": "private, no-store" },
    });
  } catch (error) {
    return productErrorResponse(error);
  }
}
export async function POST(request: NextRequest, context: Context) {
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
    const result = await configurationControl().integrationForUser(
      userID,
      (await context.params).id,
    );
    return NextResponse.json(result, {
      headers: { "Cache-Control": "private, no-store" },
    });
  } catch (error) {
    return productErrorResponse(error);
  }
}
