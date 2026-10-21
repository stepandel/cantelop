import { NextRequest, NextResponse } from "next/server";

import { cliRefreshToken } from "@core/auth/tokens";
import { productControl } from "@core/product-control";
import { getUserSession } from "@/lib/user-session";

export const dynamic = "force-dynamic";

function loopbackCallback(raw: string | null) {
  if (!raw) throw new Error("missing callback");
  const callback = new URL(raw);
  if (callback.protocol !== "http:" || !["127.0.0.1", "[::1]"].includes(callback.hostname) ||
      !callback.port || callback.pathname !== "/callback" || callback.username || callback.password ||
      callback.search || callback.hash) {
    throw new Error("invalid callback");
  }
  return callback;
}

export async function GET(request: NextRequest) {
  let callback: URL;
  const state = request.nextUrl.searchParams.get("state") ?? "";
  try {
    callback = loopbackCallback(request.nextUrl.searchParams.get("callback"));
    if (!/^[A-Za-z0-9_-]{32,128}$/.test(state)) throw new Error("invalid state");
  } catch {
    return NextResponse.json({ error: "invalid_request" }, { status: 400 });
  }
  const session = await getUserSession();
  if (!session) {
    const signIn = new URL("/api/auth/signin", request.nextUrl.origin);
    signIn.searchParams.set("callbackUrl", request.nextUrl.toString());
    return NextResponse.redirect(signIn);
  }
  try {
    await productControl().assertActiveUser(session.user.id);
  } catch {
    return NextResponse.json({ error: "unauthorized" }, { status: 401 });
  }
  const credential = cliRefreshToken(session.user.id);
  callback.hash = new URLSearchParams({ code: credential, state }).toString();
  const handoff = callback.toString().replaceAll("&", "&amp;");
  // Static inline styles only (no external fetches; CSP allows style-src
  // 'unsafe-inline' for exactly this). Mirrors the console's paper-white
  // mono idiom — this page is a step in the CLI login flow, not a UA page.
  const response = new NextResponse(`<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Cantelop login</title>
<style>
  body { margin: 0; min-height: 100dvh; display: grid; place-content: center; background: #fbfbfa; color: #17171a; font: 400 12px/1.55 ui-monospace, "SF Mono", Menlo, monospace; }
  main { max-width: 52ch; padding: 24px; }
  h1 { margin: 0; font-size: 13px; font-weight: 500; letter-spacing: -0.01em; }
  p { margin: 12px 0 0; color: #3b3b40; }
  a { display: inline-block; margin-top: 8px; padding: 6px 16px; border: 1px solid #17171a; border-radius: 4px; background: #17171a; color: #fbfbfa; text-decoration: none; font-weight: 500; }
  a:hover { background: #303035; }
</style>
<main><h1>Authorize Cantelop CLI</h1><p>A local Cantelop CLI is requesting access to your Apps for 30 days.</p>
<p><a href="${handoff}">Authorize CLI</a></p></main>`, {
    headers: { "Content-Type": "text/html; charset=utf-8" },
  });
  response.headers.set("Cache-Control", "private, no-store");
  response.headers.set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'");
  response.headers.set("Referrer-Policy", "no-referrer");
  return response;
}
