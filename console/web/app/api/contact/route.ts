import { NextResponse } from "next/server";
import { contactMessage, contactRateLimiter, sendContactEmail } from "@core/email/contact";
import { readBoundedUTF8Body } from "@core/http/bounded-body";
import { isSameConsoleOrigin } from "@/lib/console-origin";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

const allow = contactRateLimiter();

function respond(body: unknown, status = 200) {
  return NextResponse.json(body, { status, headers: { "Cache-Control": "private, no-store" } });
}

export async function POST(request: Request) {
  if (!isSameConsoleOrigin(request.headers) ||
      !request.headers.get("content-type")?.toLowerCase().startsWith("application/json")) {
    return respond({ error: "invalid_request" }, 400);
  }
  let contact;
  try {
    const value: unknown = JSON.parse(await readBoundedUTF8Body(request.body, 32_768));
    if (!value || typeof value !== "object" || Array.isArray(value)) throw new TypeError();
    contact = contactMessage(value as Record<string, unknown>);
  } catch { return respond({ error: "invalid_message" }, 400); }
  if (!contact) return respond({ sent: true });
  // Vercel sets x-real-ip to the connecting client; x-forwarded-for covers local runs.
  const client = request.headers.get("x-real-ip") ?? request.headers.get("x-forwarded-for")?.split(",")[0]?.trim() ?? "unknown";
  if (!allow(client)) return respond({ error: "rate_limited" }, 429);
  try { await sendContactEmail(contact); }
  catch (error) {
    console.error("contact email failed", error instanceof Error ? error.message : error);
    return respond({ error: "send_failed" }, 502);
  }
  return respond({ sent: true });
}
