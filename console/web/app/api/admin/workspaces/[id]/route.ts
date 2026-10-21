import { NextResponse } from "next/server";

import { controlDatabase } from "@core/db/database";
import { ProductRepository } from "@core/db/product-repository";
import { productErrorResponse } from "@core/http/response";
import { waitForWorkspaceHostArchive } from "@core/workspace-host-archive";
import { getAdminSession } from "@/lib/admin-session";
import { isSameConsoleOrigin } from "@/lib/console-origin";

export const dynamic = "force-dynamic";

// Archives the Workspace if needed and queues (or re-queues a failed) purge.
// The purge stops compute, removes filesystem state, then deletes its database.
export async function DELETE(request: Request, context: { params: Promise<{ id: string }> }) {
  if (!(await getAdminSession())) return NextResponse.json({ error: "unauthorized" }, { status: 401 });
  if (!isSameConsoleOrigin(request.headers)) return NextResponse.json({ error: "invalid_request" }, { status: 400 });
  const { id } = await context.params;
  if (!/^wsp_[0-9a-f]{32}$/.test(id)) return NextResponse.json({ error: "not_found" }, { status: 404 });
  try {
    const products = ProductRepository.fromPool(controlDatabase());
    await products.purgeWorkspaceAsOperator(id);
    // 202: the compute host may still resolve the slug to this Workspace.
    if (await waitForWorkspaceHostArchive(products, id)) return new NextResponse(null, { status: 204 });
    return new NextResponse(null, { status: 202, headers: { "Retry-After": "1" } });
  } catch (error) {
    return productErrorResponse(error);
  }
}
