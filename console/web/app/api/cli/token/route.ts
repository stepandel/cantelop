import { type NextRequest, NextResponse } from "next/server";

import { exchangeCLICredential } from "@/lib/cli-auth";
import { readBoundedUTF8Body } from "@core/http/bounded-body";

export const dynamic = "force-dynamic";

export async function POST(request: NextRequest) {
  if (!request.headers.get("content-type")?.toLowerCase().startsWith("application/json")) {
    return NextResponse.json({ error: "invalid_request" }, { status: 400 });
  }
  let refreshToken: string;
  try {
    const contentLength = Number(request.headers.get("content-length"));
    if (Number.isFinite(contentLength) && contentLength > 4096) throw new Error("invalid size");
    const document = await readBoundedUTF8Body(request.body, 4096);
    if (document.length === 0) throw new Error("invalid size");
    const input = JSON.parse(document) as Record<string, unknown>;
    if (Object.keys(input).length !== 1 || typeof input.refresh_token !== "string") throw new Error("invalid input");
    refreshToken = input.refresh_token;
  } catch {
    return NextResponse.json({ error: "invalid_request" }, { status: 400 });
  }
  try {
    const response = NextResponse.json(await exchangeCLICredential(refreshToken));
    response.headers.set("Cache-Control", "private, no-store");
    return response;
  } catch {
    return NextResponse.json({ error: "unauthorized" }, { status: 401 });
  }
}
