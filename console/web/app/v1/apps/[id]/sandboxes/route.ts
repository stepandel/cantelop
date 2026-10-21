import { type NextRequest, NextResponse } from "next/server";
import { authenticatedUserID, authenticationErrorResponse } from "@core/http/auth";
import { productErrorResponse } from "@core/http/response";
import { productControl } from "@core/product-control";
import { getCustomerSnapshot } from "@/lib/customer-telemetry";

export const dynamic = "force-dynamic";

export async function GET(request: NextRequest, context: { params: Promise<{ id: string }> }) {
  let userID: string;
  try { userID = await authenticatedUserID(request.headers.get("authorization"), "tenant:read"); }
  catch (error) { return authenticationErrorResponse(error); }
  const { id } = await context.params;
  if (!/^app_[0-9a-f]{32}$/.test(id)) return NextResponse.json({ error: "not_found" }, { status: 404 });
  try {
    // Authorize the App before querying either monitoring source.
    await productControl().getApp(userID, id);
    const response = NextResponse.json(await getCustomerSnapshot(userID, { appID: id }));
    response.headers.set("Cache-Control", "private, no-store");
    return response;
  } catch (error) {
    return productErrorResponse(error);
  }
}
