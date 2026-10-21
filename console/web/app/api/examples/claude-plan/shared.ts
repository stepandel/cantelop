import "server-only";
import { createHash, createHmac, randomBytes, timingSafeEqual } from "node:crypto";
import type { NextRequest, NextResponse } from "next/server";
import { consumeDemoRequestBudget } from "@/lib/demo-budget";
import { app } from "./claude-app";
export const DEMO_COOKIE = "__Host-cantelop-demo";
type DemoIdentity = { subject: string; cookie?: string };
function signingKey(): string {
  const serialized = process.env.CLAUDE_DEMO_PRIVATE_JWK;
  if (!serialized) throw new Error("Claude demo signing is not configured");
  return serialized;
}
function signature(identity: string): string {
  return createHmac("sha256", signingKey()).update(`cantelop-demo-cookie:${identity}`).digest("hex");
}
export async function demoIdentity(request: NextRequest, create = false): Promise<DemoIdentity | null> {
  const cookie = request.cookies.get(DEMO_COOKIE)?.value;
  if (cookie && /^[a-f0-9]{32}\.[a-f0-9]{64}$/.test(cookie)) {
    const [id, mac] = cookie.split(".");
    if (timingSafeEqual(Buffer.from(mac, "hex"), Buffer.from(signature(id), "hex")))
      return { subject: `visitor:${id}` };
  }
  if (!create) return null;
  const id = randomBytes(16).toString("hex");
  return { subject: `visitor:${id}`, cookie: `${id}.${signature(id)}` };
}
export async function admitDemoRequest(identity: DemoIdentity): Promise<boolean> {
  return consumeDemoRequestBudget(identity.subject);
}
export function withDemoCookie(response: NextResponse, identity: DemoIdentity): NextResponse {
  if (identity.cookie) response.cookies.set(DEMO_COOKIE, identity.cookie, {
    httpOnly: true, secure: true, sameSite: "lax", path: "/", maxAge: 365 * 24 * 60 * 60,
  });
  return response;
}
/** Preserve the existing visitor Workspace, originally derived from the app JWT subject. */
export function demoUserID(identity: DemoIdentity): string {
  return createHash("sha256").update(JSON.stringify(["cantelop-claude-api-owner", `blog-demo:${identity.subject}`])).digest("hex").slice(0,48);
}
export function demoWorkspace(identity: DemoIdentity) {
  return app.workspace({slug:`u-${demoUserID(identity)}`});
}
export function demoSession(identity: DemoIdentity, id: string) {
  const userID = demoUserID(identity);
  if (!id.startsWith(`${userID}:`) || !/^[a-f0-9]{48}:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(id)) return null;
  return app.workspace({slug:`u-${userID}`}).session({id,keepAliveSeconds:300});
}
