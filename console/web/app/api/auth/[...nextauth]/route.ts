import NextAuth from "next-auth";
import { headers } from "next/headers";
import { NextResponse, type NextRequest } from "next/server";

import { authOptions } from "@/lib/auth";
import { authOriginConfigurationMatches, consoleRequestOrigin } from "@/lib/console-origin";

const nextAuthHandler = NextAuth(authOptions);

async function handler(request: NextRequest, context: { params: Promise<{ nextauth: string[] }> }) {
  // Read the same headers NextAuth reads rather than deriving an origin from
  // a deployment-internal request URL. Never trust arbitrary forwarded hosts.
  const origin = consoleRequestOrigin(await headers());
  const response = !origin
    ? NextResponse.json({ error: "invalid_host" }, { status: 400 })
    : !authOriginConfigurationMatches(origin)
      ? NextResponse.json({ error: "auth_origin_configuration" }, { status: 503 })
      : await nextAuthHandler(request, context);
  // OAuth responses contain per-browser state and can arrive with a code in
  // the URL. Keep them out of caches and subsequent Referer headers.
  response.headers.set("Cache-Control", "private, no-store");
  response.headers.set("Referrer-Policy", "no-referrer");
  return response;
}

export { handler as GET, handler as POST };
