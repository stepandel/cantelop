import { randomUUID } from "node:crypto";
import { type NextRequest, NextResponse } from "next/server";

import { deploymentControl } from "@core/deployment-uploads/control";
import { authenticatedUserID, authenticationErrorResponse } from "@core/http/auth";
import { readControlJSON } from "@core/http/request";
import { productErrorResponse } from "@core/http/response";

export const dynamic = "force-dynamic";

export async function GET(request: NextRequest, context: { params: Promise<{ id: string }> }) {
  let userID: string;
  try { userID = await authenticatedUserID(request.headers.get("authorization"), "tenant:read"); }
  catch (error) { return authenticationErrorResponse(error); }
  try {
    const uploads = await deploymentControl().listUploads(userID, (await context.params).id);
    const response = NextResponse.json(uploads);
    response.headers.set("Cache-Control", "private, no-store");
    return response;
  } catch (error) {
    return productErrorResponse(error);
  }
}

export async function POST(request: NextRequest, context: { params: Promise<{ id: string }> }) {
  let userID: string;
  try { userID = await authenticatedUserID(request.headers.get("authorization"), "tenant:write"); }
  catch (error) { return authenticationErrorResponse(error); }
  const suppliedKey = request.headers.get("idempotency-key");
  if (suppliedKey !== null && !/^[A-Za-z0-9_.:-]{16,128}$/.test(suppliedKey)) {
    return NextResponse.json({ error: "invalid_request" }, { status: 400 });
  }
  try {
    const input = await readControlJSON(request, 1 << 20);
    const upload = await deploymentControl().beginUpload(
      userID,
      (await context.params).id,
      input,
      suppliedKey ?? `deploy:${randomUUID()}`,
    );
    const response = NextResponse.json(upload, { status: upload.status === "finalized" ? 200 : 201 });
    response.headers.set("Cache-Control", "private, no-store");
    return response;
  } catch (error) {
    return productErrorResponse(error);
  }
}
