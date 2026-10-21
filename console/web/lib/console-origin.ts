type RequestHeaders = Pick<Headers, "get">;

const productionOrigins = new Set([
  "https://cantelop.com",
  "https://www.cantelop.com",
  "https://console.cantelop.dev",
]);

/** Match NextAuth v4's forwarded-host precedence, but only accept our hosts. */
export function consoleRequestOrigin(headers: RequestHeaders): string | null {
  const host = headers.get("x-forwarded-host") ?? headers.get("host");
  const protocol = headers.get("x-forwarded-proto") ?? "https";
  if (!host || !["http", "https"].includes(protocol)) return null;
  const origin = `${protocol}://${host}`;
  if (productionOrigins.has(origin)) return origin;
  if (process.env.NODE_ENV !== "production" &&
      /^http:\/\/(localhost|127\.0\.0\.1)(:\d+)?$/.test(origin)) return origin;
  return null;
}

/** A browser write must originate on the same allowed host as its session. */
export function isSameConsoleOrigin(headers: RequestHeaders): boolean {
  const origin = consoleRequestOrigin(headers);
  return origin !== null && headers.get("origin") === origin;
}

export function authOriginConfigurationMatches(origin: string): boolean {
  // NEXTAUTH_URL wins over request headers in v4. Fail closed if an old fixed
  // value would send OAuth to a different host and lose its state cookies.
  if (process.env.NEXTAUTH_URL) return process.env.NEXTAUTH_URL === origin;
  if (process.env.VERCEL ?? process.env.AUTH_TRUST_HOST) return true;
  return origin === "http://localhost:3000";
}
