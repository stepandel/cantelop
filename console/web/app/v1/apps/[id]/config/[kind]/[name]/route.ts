import { type NextRequest, NextResponse } from "next/server";

import { configurationControl } from "@core/configuration/control";
import { maxConfigMutationRequestBytes } from "@core/configuration/limits";
import type { ConfigValueKind } from "@core/configuration/repository";
import { authenticatedUserID, authenticationErrorResponse } from "@core/http/auth";
import { readControlJSON } from "@core/http/request";
import { productErrorResponse } from "@core/http/response";

export const dynamic = "force-dynamic";

type Context = { params: Promise<{ id: string; kind: string; name: string }> };

export async function PUT(request: NextRequest, context: Context) {
  let userID: string;
  try { userID = await authenticatedUserID(request.headers.get("authorization"), "tenant:write"); }
  catch (error) { return authenticationErrorResponse(error); }
  try {
    const { id, kind, name } = await context.params;
    const valueKind = parsedKind(kind);
    const document = await readControlJSON(request, maxConfigMutationRequestBytes);
    if (typeof document !== "object" || document === null || Array.isArray(document) ||
        Object.keys(document).join(",") !== "value" || typeof (document as { value?: unknown }).value !== "string") {
      throw new TypeError("invalid configuration request");
    }
    const response = NextResponse.json(await configurationControl().setForUser(
      userID, id, valueKind, name, (document as { value: string }).value,
    ));
    response.headers.set("Cache-Control", "private, no-store");
    return response;
  } catch (error) {
    return productErrorResponse(error);
  }
}

export async function DELETE(request: NextRequest, context: Context) {
  let userID: string;
  try { userID = await authenticatedUserID(request.headers.get("authorization"), "tenant:write"); }
  catch (error) { return authenticationErrorResponse(error); }
  try {
    const { id, kind, name } = await context.params;
    await configurationControl().deleteForUser(userID, id, parsedKind(kind), name);
    return new NextResponse(null, { status: 204 });
  } catch (error) {
    return productErrorResponse(error);
  }
}

function parsedKind(segment: string): ConfigValueKind {
  if (segment === "variables") return "variable";
  if (segment === "secrets") return "secret";
  throw new TypeError("invalid configuration kind");
}
