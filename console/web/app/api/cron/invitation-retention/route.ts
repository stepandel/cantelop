import { timingSafeEqual } from "node:crypto";
import { controlDatabase } from "@core/db/database";
import { purgeExpiredInvitations } from "@core/email/maintenance";
export const runtime = "nodejs";
export const dynamic = "force-dynamic";
export async function GET(request: Request) {
  const secret = process.env.CRON_SECRET;
  if (!secret) return new Response("Not configured", { status: 503 });
  const expected = Buffer.from(`Bearer ${secret}`);
  const supplied = Buffer.from(request.headers.get("authorization") ?? "");
  if (expected.length !== supplied.length || !timingSafeEqual(expected, supplied)) return new Response("Unauthorized", { status: 401 });
  return Response.json({ deleted: await purgeExpiredInvitations(controlDatabase()) }, { headers: { "Cache-Control": "no-store" } });
}
