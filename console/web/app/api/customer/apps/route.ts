import { type NextRequest, NextResponse } from "next/server";

import { productControl } from "@core/product-control";
import { readBoundedUTF8Body } from "@core/http/bounded-body";
import { productErrorResponse } from "@core/http/response";
import { customerAppSpec } from "@/lib/customer-app-input";
import { isSameConsoleOrigin } from "@/lib/console-origin";
import { getUserSession } from "@/lib/user-session";

export const dynamic = "force-dynamic";

export async function POST(request: NextRequest) {
  const session = await getUserSession();
  if (!session) return NextResponse.json({ error: "unauthorized" }, { status: 401 });
  if (!isSameConsoleOrigin(request.headers) ||
      !request.headers.get("content-type")?.toLowerCase().startsWith("application/json")) {
    return NextResponse.json({ error: "invalid_request" }, { status: 400 });
  }
  let input: ReturnType<typeof customerAppSpec>;
  try {
    const document = await readBoundedUTF8Body(request.body, 4 * 16 * 1024);
    if (document.length === 0 || document.length > 16 * 1024) throw new Error("invalid size");
    input = customerAppSpec(JSON.parse(document));
  } catch {
    return NextResponse.json({ error: "invalid_request" }, { status: 400 });
  }
  try {
    const response = NextResponse.json(await productControl().createApp(session.user.id, input), { status: 201 });
    response.headers.set("Cache-Control", "private, no-store");
    return response;
  } catch (error) {
    return productErrorResponse(error);
  }
}
