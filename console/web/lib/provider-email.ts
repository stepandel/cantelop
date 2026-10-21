import { createHash } from "node:crypto";

import type { GithubEmail, GithubProfile } from "next-auth/providers/github";
import type { GoogleProfile } from "next-auth/providers/google";

export function verifiedGithubEmail(emails: unknown): string | null {
  // A missing user:email grant or an API error arrives as an object, not a
  // list; that is an unverified address, not a crash.
  if (!Array.isArray(emails)) return null;
  return (emails as GithubEmail[]).find((email) => email.primary && email.verified)?.email
    ?? emails.find((email) => email.verified)?.email
    ?? null;
}

/** What a session carries to ask its provider for the address again. */
export type ProviderCredentials = {
  provider?: string;
  subject?: string;
  accessToken?: string;
  /** Epoch seconds, as OAuth reports it. */
  accessTokenExpiresAt?: number;
  refreshToken?: string;
};

// Every session read runs the jwt callback, and pages poll API routes every
// few seconds, so a provider round-trip per read would put GitHub or Google on
// the latency path of the whole console. A confirmed address is trusted for a
// few minutes; a failure is retried sooner so an outage recovers quickly.
const VERIFIED_TTL_MS = 5 * 60_000;
const FAILED_TTL_MS = 30_000;
const REQUEST_TIMEOUT_MS = 5_000;
const MAX_ENTRIES = 10_000;
// Refresh a Google access token this long before it expires.
const EXPIRY_MARGIN_MS = 60_000;

type Check = { expiresAt: number; email: Promise<string | null> };
const checks = new Map<string, Check>();
// getServerSession never rewrites the session cookie, so a refreshed Google
// access token cannot travel in the JWT; it lives here for its lifetime.
const googleAccess = new Map<string, { token: string; expiresAt: number }>();

export function clearProviderEmailCache() {
  checks.clear();
  googleAccess.clear();
}

function remember<V>(map: Map<string, V>, key: string, value: V) {
  map.delete(key);
  map.set(key, value);
  if (map.size > MAX_ENTRIES) map.delete(map.keys().next().value!);
}

// Keyed by the session's own credentials, so a fresh sign-in is checked anew
// and no two sessions share an answer.
function sessionKey(credentials: ProviderCredentials): string {
  return createHash("sha256")
    .update(`${credentials.provider}\0${credentials.subject}\0${credentials.accessToken}`)
    .digest("hex");
}

export function currentVerifiedEmail(credentials: ProviderCredentials): Promise<string | null> {
  if (!credentials.provider || !credentials.subject || !credentials.accessToken) return Promise.resolve(null);
  const key = sessionKey(credentials);
  const cached = checks.get(key);
  if (cached && cached.expiresAt > Date.now()) return cached.email;
  // Keep ordinary account access on a failure, but deny email-based
  // authorization until the provider can confirm ownership again.
  const email = lookup(credentials, key).catch((error: unknown) => {
    console.warn("provider_email_lookup_failed", error instanceof Error ? error.message : error);
    return null;
  });
  const check = { expiresAt: Date.now() + VERIFIED_TTL_MS, email };
  remember(checks, key, check);
  void email.then((value) => {
    if (value === null) check.expiresAt = Math.min(check.expiresAt, Date.now() + FAILED_TTL_MS);
  });
  return email;
}

function providerRequest(url: string, accessToken: string) {
  return fetch(url, {
    headers: { Authorization: `Bearer ${accessToken}`, Accept: "application/json" },
    cache: "no-store",
    signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS),
  });
}

async function lookup(credentials: ProviderCredentials, key: string): Promise<string | null> {
  const { provider, subject } = credentials;
  if (provider === "github") {
    const [profile, emails] = await Promise.all([
      providerRequest("https://api.github.com/user", credentials.accessToken!),
      providerRequest("https://api.github.com/user/emails", credentials.accessToken!),
    ]);
    if (!profile.ok || String((await profile.json() as GithubProfile).id) !== subject) return null;
    return emails.ok ? verifiedGithubEmail(await emails.json()) : null;
  }
  if (provider === "google") {
    const accessToken = await googleAccessToken(credentials, key);
    if (!accessToken) return null;
    const response = await providerRequest("https://openidconnect.googleapis.com/v1/userinfo", accessToken);
    if (!response.ok) return null;
    const profile = await response.json() as GoogleProfile;
    return profile.sub === subject && profile.email_verified === true ? profile.email : null;
  }
  return null;
}

// Google access tokens last an hour; the refresh token granted at sign-in
// keeps the check working for the rest of the session.
async function googleAccessToken(credentials: ProviderCredentials, key: string): Promise<string | null> {
  const now = Date.now();
  const refreshed = googleAccess.get(key);
  if (refreshed && refreshed.expiresAt - EXPIRY_MARGIN_MS > now) return refreshed.token;
  const expiresAt = credentials.accessTokenExpiresAt ? credentials.accessTokenExpiresAt * 1000 : null;
  if (expiresAt === null || expiresAt - EXPIRY_MARGIN_MS > now) return credentials.accessToken!;
  if (!credentials.refreshToken || !process.env.GOOGLE_ID || !process.env.GOOGLE_SECRET) return null;
  const response = await fetch("https://oauth2.googleapis.com/token", {
    method: "POST",
    body: new URLSearchParams({
      client_id: process.env.GOOGLE_ID,
      client_secret: process.env.GOOGLE_SECRET,
      grant_type: "refresh_token",
      refresh_token: credentials.refreshToken,
    }),
    cache: "no-store",
    signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS),
  });
  if (!response.ok) return null;
  const body = await response.json() as { access_token?: unknown; expires_in?: unknown };
  if (typeof body.access_token !== "string") return null;
  const lifetime = typeof body.expires_in === "number" ? body.expires_in : 3600;
  remember(googleAccess, key, { token: body.access_token, expiresAt: now + lifetime * 1000 });
  return body.access_token;
}
