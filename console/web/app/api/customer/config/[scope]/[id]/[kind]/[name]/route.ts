import { type NextRequest, NextResponse } from "next/server";

import { configurationControl } from "@core/configuration/control";
import { readBoundedUTF8Body } from "@core/http/bounded-body";
import type { ConfigValueKind } from "@core/configuration/repository";
import { productErrorResponse } from "@core/http/response";
import {
  configNamePattern,
  configScopeIDPatterns,
  isConfigKindSegment,
  isConfigScope,
  maxConfigValueBytes,
} from "@/lib/config-values";
import { isSameConsoleOrigin } from "@/lib/console-origin";
import { getUserSession } from "@/lib/user-session";

export const dynamic = "force-dynamic";

type Params = { params: Promise<{ scope: string; id: string; kind: string; name: string }> };

async function validated(request: NextRequest, context: Params) {
  const session = await getUserSession();
  if (!session) return { error: NextResponse.json({ error: "unauthorized" }, { status: 401 }) };
  if (!isSameConsoleOrigin(request.headers)) {
    return { error: NextResponse.json({ error: "invalid_request" }, { status: 400 }) };
  }
  const { scope, id, kind, name } = await context.params;
  if (!isConfigScope(scope) || !configScopeIDPatterns[scope].test(id) ||
      !isConfigKindSegment(kind) || !configNamePattern.test(name)) {
    return { error: NextResponse.json({ error: "not_found" }, { status: 404 }) };
  }
  return { session, scope, id, kind, name } as const;
}

export async function PUT(request: NextRequest, context: Params) {
  const input = await validated(request, context);
  if ("error" in input) return input.error;
  if (!request.headers.get("content-type")?.toLowerCase().startsWith("application/json")) {
    return NextResponse.json({ error: "invalid_request" }, { status: 400 });
  }
  let value: string;
  try {
    const document = await readBoundedUTF8Body(request.body, 4 * (maxConfigValueBytes + 1024));
    if (document.length === 0 || document.length > maxConfigValueBytes + 1024) throw new Error("invalid size");
    const parsed = JSON.parse(document) as unknown;
    if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed) ||
        Object.keys(parsed).length !== 1 || typeof (parsed as { value?: unknown }).value !== "string") {
      throw new Error("invalid document");
    }
    value = (parsed as { value: string }).value;
    if (Buffer.byteLength(value) > maxConfigValueBytes || value.includes("\0")) throw new Error("invalid value");
  } catch {
    return NextResponse.json({ error: "invalid_request" }, { status: 400 });
  }
  try {
    const response = NextResponse.json(await configurationControl().setForUser(
      input.session.user.id, input.id, valueKind(input.kind), input.name, value,
    ));
    response.headers.set("Cache-Control", "private, no-store");
    return response;
  } catch (error) {
    return productErrorResponse(error);
  }
}

export async function DELETE(request: NextRequest, context: Params) {
  const input = await validated(request, context);
  if ("error" in input) return input.error;
  try {
    await configurationControl().deleteForUser(input.session.user.id, input.id, valueKind(input.kind), input.name);
    return new NextResponse(null, { status: 204 });
  } catch (error) {
    return productErrorResponse(error);
  }
}

function valueKind(segment: "variables" | "secrets"): ConfigValueKind {
  return segment === "variables" ? "variable" : "secret";
}
