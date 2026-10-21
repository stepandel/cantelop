import { type NextRequest, NextResponse } from "next/server";

import { authenticatedUserID, authenticationErrorResponse } from "@core/http/auth";
import { productErrorResponse } from "@core/http/response";
import { listCustomerLogs } from "@/lib/customer-operations";

export const dynamic = "force-dynamic";

export async function GET(request: NextRequest, context: { params: Promise<{ id: string }> }) {
  let userID: string;
  try { userID = await authenticatedUserID(request.headers.get("authorization"), "tenant:read"); }
  catch (error) { return authenticationErrorResponse(error); }
  try {
    const incoming = request.nextUrl.searchParams;
    for (const key of incoming.keys()) {
      if ((key !== "before_sequence" && key !== "after_sequence" && key !== "limit") || incoming.getAll(key).length !== 1) {
        throw new TypeError("invalid log query");
      }
    }
    const before = incoming.get("before_sequence");
    const after = incoming.get("after_sequence");
    const limit = incoming.get("limit") ?? "100";
    const cursor = (value: string | null) => value === null || (/^\d+$/.test(value) && Number(value) >= 1);
    if (!cursor(before) || !cursor(after) || (before !== null && after !== null) ||
        !/^\d+$/.test(limit) || Number(limit) < 1 || Number(limit) > 500) {
      throw new TypeError("invalid log query");
    }
    const search = new URLSearchParams({ limit });
    if (before) search.set("before_sequence", before);
    if (after) search.set("after_sequence", after);
    const response = NextResponse.json(await listCustomerLogs(userID, (await context.params).id, search));
    response.headers.set("Cache-Control", "private, no-store");
    return response;
  } catch (error) {
    return productErrorResponse(error);
  }
}
