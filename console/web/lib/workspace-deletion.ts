import { NextResponse } from "next/server";

import { productErrorResponse } from "@core/http/response";
import { productControl } from "@core/product-control";

/** Deletes a Workspace the user can access. 202 means the compute host may
 *  still resolve the slug; 204 means host admission is closed. Neither means
 *  the queued filesystem/database purge has finished. */
export async function deleteWorkspaceResponse(userID: string, id: string): Promise<NextResponse> {
  if (!/^wsp_[0-9a-f]{32}$/.test(id)) return NextResponse.json({ error: "not_found" }, { status: 404 });
  try {
    const hostArchived = await productControl().deleteWorkspace(userID, id);
    return new NextResponse(null, {
      status: hostArchived ? 204 : 202,
      headers: { "Cache-Control": "private, no-store", ...(hostArchived ? {} : { "Retry-After": "1" }) },
    });
  } catch (error) {
    return productErrorResponse(error);
  }
}
