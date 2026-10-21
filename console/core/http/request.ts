import type { NextRequest } from "next/server";

import { readBoundedUTF8Body } from "./bounded-body";

export async function readControlJSON(request: NextRequest, maxBytes: number): Promise<unknown> {
  if (!request.headers.get("content-type")?.toLowerCase().startsWith("application/json")) throw new TypeError("invalid content type");
  const contentLength = Number(request.headers.get("content-length"));
  if (Number.isFinite(contentLength) && (contentLength < 0 || contentLength > maxBytes)) throw new TypeError("invalid body size");
  try {
    const document = await readBoundedUTF8Body(request.body, maxBytes);
    if (!document) throw new TypeError("invalid body size");
    return JSON.parse(document) as unknown;
  } catch {
    throw new TypeError("invalid JSON body");
  }
}
