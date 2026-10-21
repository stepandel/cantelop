import { afterEach, expect, test } from "bun:test";
import { getRedirectUrl, unstable_getResponseFromNextConfig } from "next/experimental/testing/server";
import { AuthHandler } from "../../node_modules/next-auth/core/index.js";
import GitHubProvider from "next-auth/providers/github";
import GoogleProvider from "next-auth/providers/google";
import { encode } from "next-auth/jwt";
import nextConfig from "../next.config.ts";
import { authOriginConfigurationMatches, consoleRequestOrigin, isSameConsoleOrigin } from "../lib/console-origin.ts";

const originalEnv = { ...process.env };
afterEach(() => { process.env = { ...originalEnv }; });
const origins = ["https://cantelop.com", "https://console.cantelop.dev", "https://www.cantelop.com"];
const options = {
  secret: "test-shared-secret-for-both-domains",
  providers: [GitHubProvider({ clientId: "github-id", clientSecret: "github-secret" }),
    GoogleProvider({ clientId: "google-id", clientSecret: "google-secret" })],
  session: { strategy: "jwt" },
};
function requestHeaders(origin) {
  const url = new URL(origin);
  return { host: url.host, "x-forwarded-host": url.host, "x-forwarded-proto": url.protocol.slice(0, -1) };
}
function configure() {
  delete process.env.NEXTAUTH_URL;
  process.env.VERCEL = "1";
  process.env.NODE_ENV = "production";
}

test.each(origins)("%s uses local auth URLs and expected page routing", async (origin) => {
  configure();
  const response = await unstable_getResponseFromNextConfig({ url: `${origin}/profile`, nextConfig });
  expect(getRedirectUrl(response)).toBe(origin === "https://www.cantelop.com" ? "https://cantelop.com/profile" : null);
  const headers = requestHeaders(origin);
  expect(consoleRequestOrigin(new Headers(headers))).toBe(origin);
  expect(authOriginConfigurationMatches(origin)).toBe(true);
  const providers = await AuthHandler({ options, req: { action: "providers", method: "GET", headers } });
  for (const id of ["github", "google"]) {
    expect(providers.body[id].callbackUrl).toBe(`${origin}/api/auth/callback/${id}`);
    expect(providers.body[id].signinUrl).toBe(`${origin}/api/auth/signin/${id}`);
  }
  const csrf = await AuthHandler({ options, req: { action: "csrf", method: "GET", headers } });
  expect(csrf.body.csrfToken).toBeTruthy();
  for (const cookie of csrf.cookies) {
    expect(cookie.options.domain).toBeUndefined();
    expect(cookie.options.secure).toBe(true);
  }
  const jwt = await encode({ secret: options.secret, token: { name: "Test", email: "test@example.com" } });
  const session = await AuthHandler({ options, req: { action: "session", method: "GET", headers,
    cookies: { "__Secure-next-auth.session-token": jwt } } });
  expect(session.body.user.email).toBe("test@example.com");
  const csrfCookie = csrf.cookies.find((cookie) => cookie.name === "__Host-next-auth.csrf-token");
  const signout = await AuthHandler({ options, req: { action: "signout", method: "POST", headers,
    cookies: { [csrfCookie.name]: csrfCookie.value, "__Secure-next-auth.session-token": jwt },
    body: { csrfToken: csrf.body.csrfToken, callbackUrl: `${origin}/` } } });
  expect(signout.redirect).toBe(`${origin}/`);
  expect(signout.cookies.some((cookie) => cookie.name === "__Secure-next-auth.session-token" && cookie.value === "")).toBe(true);
});

test.each(origins)("customer writes on %s require a matching browser origin", (origin) => {
  configure();
  const headers = new Headers(requestHeaders(origin));
  headers.set("origin", origin);
  expect(isSameConsoleOrigin(headers)).toBe(true);
  for (const other of [origins.find((value) => value !== origin), "https://evil.example", "null", `${origin}/`]) {
    headers.set("origin", other);
    expect(isSameConsoleOrigin(headers)).toBe(false);
  }
  headers.delete("origin");
  expect(isSameConsoleOrigin(headers)).toBe(false);
});

test("rejects forged hosts, multiple forwarded hosts, and insecure production origins", () => {
  configure();
  for (const host of ["evil.example", "cantelop.com.evil.example", "cantelop.com, evil.example", "cantelop.com@evil.example", "cantelop.com:444"]) {
    expect(consoleRequestOrigin(new Headers({ host: "cantelop.com", "x-forwarded-host": host }))).toBeNull();
  }
  expect(consoleRequestOrigin(new Headers({ host: "cantelop.com", "x-forwarded-proto": "http" }))).toBeNull();
  expect(consoleRequestOrigin(new Headers({ host: "localhost:3000", "x-forwarded-proto": "http" }))).toBeNull();
});

test("a stale fixed auth URL cannot silently move a browser to the other domain", () => {
  configure();
  process.env.NEXTAUTH_URL = origins[1];
  expect(authOriginConfigurationMatches(origins[0])).toBe(false);
  expect(authOriginConfigurationMatches(origins[1])).toBe(true);
});

test("supports explicit local development", () => {
  process.env.NODE_ENV = "development";
  process.env.NEXTAUTH_URL = "http://localhost:3000";
  const headers = new Headers({ host: "localhost:3000", "x-forwarded-proto": "http", origin: "http://localhost:3000" });
  expect(isSameConsoleOrigin(headers)).toBe(true);
  expect(authOriginConfigurationMatches("http://localhost:3000")).toBe(true);
});
