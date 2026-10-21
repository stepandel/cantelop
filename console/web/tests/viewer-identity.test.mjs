import { beforeAll, beforeEach, expect, mock, test } from "bun:test";

import { viewerName, viewerOf } from "../lib/viewer.ts";

test("the provider login is the name when the session carries one", () => {
  expect(viewerName({ login: "octocat", email: "octocat@example.com", avatarUrl: null })).toBe("octocat");
});

test("the viewer reads login, email and avatar off the session user", () => {
  expect(viewerOf({
    user: { id: "usr_1", login: "octocat", email: "octocat@example.com", image: "https://avatars.example/1" },
  })).toEqual({
    login: "octocat",
    email: "octocat@example.com",
    avatarUrl: "https://avatars.example/1",
  });
});

// The auth wiring: the login has to survive the provider mapping, the jwt, and
// the session, or the session is rejected and the user signs in again.
let authOptions;
let verifiedEmailAuthOptions;
let googleLogin;
let verifiedGithubEmail;
let parseProviders;
let clearProviderEmailCache;
const resolved = [];
let sessionIdentity;
const sessionSubjects = [];

beforeAll(async () => {
  process.env.GITHUB_ID = "test-id";
  process.env.GITHUB_SECRET = "test-secret";
  process.env.GOOGLE_ID = "test-google-id";
  process.env.GOOGLE_SECRET = "test-google-secret";
  mock.module("../../core/auth/identity.ts", () => ({
    resolveSessionIdentity: async (id) => {
      sessionSubjects.push(id);
      if (sessionIdentity instanceof Error) throw sessionIdentity;
      return sessionIdentity;
    },
    resolveUserIdentity: async (provider, providerSubject, email) => {
      resolved.push({ provider, providerSubject, email });
      return { id: "usr_00000000000000000000000000000001" };
    },
  }));
  ({ authOptions, verifiedEmailAuthOptions, googleLogin, verifiedGithubEmail } = await import("../lib/auth.ts"));
  ({ clearProviderEmailCache } = await import("../lib/provider-email.ts"));
  // Deep path: next-auth does not export its provider parser, and asserting on
  // the raw config instead would pass even if the override never ran.
  parseProviders = (await import("../../node_modules/next-auth/core/lib/providers.js")).default;
});

const githubProfile = {
  id: 583231,
  login: "octocat",
  name: "The Octocat",
  email: "octocat@example.com",
  email_verified: true,
  avatar_url: "https://avatars.githubusercontent.com/u/583231?v=4",
};

const googleProfile = {
  sub: "108000000000000000001",
  name: "Ada Lovelace",
  email: "ada.lovelace@example.com",
  email_verified: true,
  picture: "https://lh3.googleusercontent.com/a/ada",
};

// next-auth applies a provider's user options over the built-in defaults when
// it parses them, so the mapping that actually runs is the merged one — asserting
// on the raw provider object would pass while the login silently never lands.
function mergedProvider(id) {
  const { providers } = parseProviders({
    providers: authOptions.providers,
    url: new URL("https://console.test/api/auth"),
    providerId: id,
  });
  return providers.find((provider) => provider.id === id);
}

test("GitHub and Google are both offered, in that order", () => {
  expect(authOptions.providers.map((provider) => provider.id)).toEqual(["github", "google"]);
});

test("the GitHub provider keeps the login alongside name, email and avatar", () => {
  expect(mergedProvider("github").profile(githubProfile)).toEqual({
    id: "583231",
    login: "octocat",
    name: "The Octocat",
    email: "octocat@example.com",
    image: "https://avatars.githubusercontent.com/u/583231?v=4",
  });
});

test("the Google provider derives a login from the address and keeps the picture", () => {
  expect(mergedProvider("google").profile(googleProfile)).toEqual({
    id: "108000000000000000001",
    login: "ada.lovelace",
    name: "Ada Lovelace",
    email: "ada.lovelace@example.com",
    image: "https://lh3.googleusercontent.com/a/ada",
  });
});

test("GitHub linking uses a verified primary address and rejects unverified addresses", () => {
  expect(verifiedGithubEmail([
    { email: "old@example.com", primary: false, verified: true, visibility: "private" },
    { email: "current@example.com", primary: true, verified: true, visibility: "private" },
  ])).toBe("current@example.com");
  expect(verifiedGithubEmail([
    { email: "unverified@example.com", primary: true, verified: false, visibility: "private" },
  ])).toBeNull();
  expect(verifiedGithubEmail({ message: "Resource not accessible by integration" })).toBeNull();
});

test("the GitHub userinfo request replaces the public email with GitHub's verified primary address", async () => {
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () => new Response(JSON.stringify([
    { email: "public@example.com", primary: false, verified: false, visibility: "public" },
    { email: "verified@example.com", primary: true, verified: true, visibility: "private" },
  ]));
  try {
    const profile = await mergedProvider("github").userinfo.request({
      client: { userinfo: async () => githubProfile },
      tokens: { access_token: "github-token" },
      provider: mergedProvider("github"),
    });
    expect(profile.email).toBe("verified@example.com");
    expect(profile.email_verified).toBe(true);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("a Google login falls back from the address to the name to the subject", () => {
  expect(googleLogin({ sub: "1", email: "ada@example.com", name: "Ada" })).toBe("ada");
  expect(googleLogin({ sub: "1", email: undefined, name: " Ada " })).toBe("Ada");
  expect(googleLogin({ sub: "1", email: "", name: "" })).toBe("1");
});

test("only provider-verified non-empty addresses can sign in", async () => {
  const account = { provider: "google", providerAccountId: googleProfile.sub };
  expect(await authOptions.callbacks.signIn({ account, profile: googleProfile })).toBe(true);
  expect(await authOptions.callbacks.signIn({ account, profile: { ...googleProfile, email_verified: false } })).toBe(false);
  expect(await authOptions.callbacks.signIn({ account, profile: { ...googleProfile, email_verified: undefined } })).toBe(false);
  expect(await authOptions.callbacks.signIn({ account: { provider: "github", providerAccountId: "583231" }, profile: githubProfile })).toBe(true);
  expect(await authOptions.callbacks.signIn({
    account: { provider: "github", providerAccountId: "583231" },
    profile: { ...githubProfile, email_verified: false },
  })).toBe(false);
  expect(await authOptions.callbacks.signIn({
    account: { provider: "credentials", providerAccountId: "x" }, profile: {},
  })).toBe(false);
});

test("signing in writes the login and the resolved user id into the token", async () => {
  resolved.length = 0;
  const token = await authOptions.callbacks.jwt({
    token: { email: "octocat@example.com" },
    account: { provider: "github", providerAccountId: "583231" },
    user: { id: "583231", login: "octocat" },
  });

  expect(token.login).toBe("octocat");
  expect(token.userId).toBe("usr_00000000000000000000000000000001");
  expect(resolved).toEqual([{ provider: "github", providerSubject: "583231", email: "octocat@example.com" }]);
});

test("a Google sign-in resolves the identity under the google provider", async () => {
  resolved.length = 0;
  const token = await authOptions.callbacks.jwt({
    token: { email: googleProfile.email },
    account: { provider: "google", providerAccountId: googleProfile.sub },
    user: { id: googleProfile.sub, login: "ada.lovelace" },
  });

  expect(token.login).toBe("ada.lovelace");
  expect(token.userId).toBe("usr_00000000000000000000000000000001");
  expect(resolved).toEqual([{ provider: "google", providerSubject: googleProfile.sub, email: googleProfile.email }]);
});

beforeEach(() => clearProviderEmailCache?.());

test("a refreshed product email cannot replace the provider-verified admin claim", async () => {
  const token = await authOptions.callbacks.jwt({
    token: { email: "changed@example.com" },
    account: { provider: "github", providerAccountId: "583231", access_token: "provider-token" },
    user: { id: "583231", login: "octocat" },
  });
  expect(token.verifiedEmail).toBe("changed@example.com");
  sessionIdentity = { id: token.userId, email: "admin@example.com" };
  const previousFetch = globalThis.fetch;
  globalThis.fetch = async (url) => Response.json(
    String(url).endsWith("/user")
      ? { id: 583231 }
      : [{ email: "changed@example.com", verified: true, primary: true }],
  );
  let refreshed;
  try { refreshed = await verifiedEmailAuthOptions.callbacks.jwt({ token }); }
  finally { globalThis.fetch = previousFetch; }
  expect(refreshed.email).toBe("admin@example.com");
  expect(refreshed.verifiedEmail).toBe("changed@example.com");
  const session = await authOptions.callbacks.session({
    session: { user: { email: refreshed.email } }, token: refreshed,
  });
  expect(session.user.email).toBe("admin@example.com");
  expect(session.user.verifiedEmail).toBe("changed@example.com");
  expect(session.user.providerAccessToken).toBeUndefined();
});

test("a provider email change or outage revokes email-based authorization on refresh", async () => {
  const token = await authOptions.callbacks.jwt({
    token: { email: "admin@example.com" },
    account: { provider: "github", providerAccountId: "583231", access_token: "provider-token" },
    user: { id: "583231", login: "octocat" },
  });
  sessionIdentity = { id: token.userId, email: "admin@example.com" };
  const previousFetch = globalThis.fetch;
  try {
    globalThis.fetch = async (url) => Response.json(
      String(url).endsWith("/user")
        ? { id: 583231 }
        : [{ email: "changed@example.com", verified: true, primary: true }],
    );
    expect((await verifiedEmailAuthOptions.callbacks.jwt({ token })).verifiedEmail).toBe("changed@example.com");
    clearProviderEmailCache();
    globalThis.fetch = async () => { throw new Error("provider unavailable"); };
    expect((await verifiedEmailAuthOptions.callbacks.jwt({ token })).verifiedEmail).toBeNull();
  } finally { globalThis.fetch = previousFetch; }
});

test("Google email revalidation stays bound to the signed-in provider subject", async () => {
  const token = await authOptions.callbacks.jwt({
    token: { email: "admin@example.com" },
    account: { provider: "google", providerAccountId: "google-subject", access_token: "provider-token" },
    user: { id: "google-subject", login: "admin" },
  });
  sessionIdentity = { id: token.userId, email: "admin@example.com" };
  const previousFetch = globalThis.fetch;
  try {
    globalThis.fetch = async () => Response.json({ sub: "another-subject", email: "admin@example.com", email_verified: true });
    expect((await verifiedEmailAuthOptions.callbacks.jwt({ token })).verifiedEmail).toBeNull();
    clearProviderEmailCache();
    globalThis.fetch = async () => Response.json({ sub: "google-subject", email: "admin@example.com", email_verified: true });
    expect((await verifiedEmailAuthOptions.callbacks.jwt({ token })).verifiedEmail).toBe("admin@example.com");
  } finally { globalThis.fetch = previousFetch; }
});

test("provider email revalidation is cached instead of repeated on every session read", async () => {
  const token = await authOptions.callbacks.jwt({
    token: { email: "admin@example.com" },
    account: { provider: "github", providerAccountId: "583231", access_token: "cached-token" },
    user: { id: "583231", login: "octocat" },
  });
  sessionIdentity = { id: token.userId, email: "admin@example.com" };
  const previousFetch = globalThis.fetch;
  let requests = 0;
  try {
    globalThis.fetch = async (url) => {
      requests += 1;
      return Response.json(String(url).endsWith("/user")
        ? { id: 583231 }
        : [{ email: "admin@example.com", verified: true, primary: true }]);
    };
    const reads = await Promise.all([1, 2, 3].map(() => verifiedEmailAuthOptions.callbacks.jwt({ token: { ...token } })));
    expect(reads.map((read) => read.verifiedEmail)).toEqual(["admin@example.com", "admin@example.com", "admin@example.com"]);
    expect((await verifiedEmailAuthOptions.callbacks.jwt({ token: { ...token } })).verifiedEmail).toBe("admin@example.com");
    expect(requests).toBe(2);
  } finally { globalThis.fetch = previousFetch; }
});

test("an expired Google access token is refreshed before revalidating the address", async () => {
  const token = await authOptions.callbacks.jwt({
    token: { email: "admin@example.com" },
    account: {
      provider: "google", providerAccountId: "google-subject", access_token: "expired-token",
      expires_at: Math.floor(Date.now() / 1000) - 10, refresh_token: "refresh-token",
    },
    user: { id: "google-subject", login: "admin" },
  });
  expect(token.providerRefreshToken).toBe("refresh-token");
  sessionIdentity = { id: token.userId, email: "admin@example.com" };
  const previousFetch = globalThis.fetch;
  const calls = [];
  try {
    globalThis.fetch = async (url, init) => {
      calls.push({ url: String(url), authorization: init?.headers?.Authorization, body: init?.body?.toString() });
      if (String(url) === "https://oauth2.googleapis.com/token") return Response.json({ access_token: "fresh-token", expires_in: 3599 });
      return Response.json({ sub: "google-subject", email: "admin@example.com", email_verified: true });
    };
    expect((await verifiedEmailAuthOptions.callbacks.jwt({ token })).verifiedEmail).toBe("admin@example.com");
    expect(calls[0].url).toBe("https://oauth2.googleapis.com/token");
    expect(calls[0].body).toContain("grant_type=refresh_token");
    expect(calls[0].body).toContain("refresh_token=refresh-token");
    expect(calls[1].authorization).toBe("Bearer fresh-token");
    // The confirmed address is reused; the provider is not asked again.
    expect((await verifiedEmailAuthOptions.callbacks.jwt({ token })).verifiedEmail).toBe("admin@example.com");
    expect(calls).toHaveLength(2);
  } finally { globalThis.fetch = previousFetch; }
});

test("a session from before provider credentials were stored has no verified address", async () => {
  sessionIdentity = { id: "usr_00000000000000000000000000000001", email: "admin@example.com" };
  const token = await verifiedEmailAuthOptions.callbacks.jwt({
    token: { userId: "usr_00000000000000000000000000000001", email: "admin@example.com", login: "admin" },
  });
  expect(token.verifiedEmail).toBeNull();
});

test("an ordinary session read never asks the provider and drops the sign-in claim", async () => {
  const token = await authOptions.callbacks.jwt({
    token: { email: "admin@example.com" },
    account: { provider: "github", providerAccountId: "583231", access_token: "provider-token" },
    user: { id: "583231", login: "octocat" },
  });
  expect(token.verifiedEmail).toBe("admin@example.com");
  sessionIdentity = { id: token.userId, email: "admin@example.com" };
  const previousFetch = globalThis.fetch;
  let requests = 0;
  try {
    globalThis.fetch = async () => { requests += 1; return Response.json({}); };
    const refreshed = await authOptions.callbacks.jwt({ token: { ...token } });
    expect(refreshed.userId).toBe(token.userId);
    expect(refreshed.verifiedEmail).toBeUndefined();
    const session = await authOptions.callbacks.session({ session: { user: { email: refreshed.email } }, token: refreshed });
    expect(session.user.verifiedEmail).toBeNull();
    expect(requests).toBe(0);
  } finally { globalThis.fetch = previousFetch; }
});

test("an unknown provider never reaches identity resolution", async () => {
  resolved.length = 0;
  const token = await authOptions.callbacks.jwt({
    token: { email: "someone@example.com" },
    account: { provider: "credentials", providerAccountId: "x" },
    user: { id: "x", login: "someone" },
  });

  expect(token.userId).toBeUndefined();
  expect(resolved).toEqual([]);
});

test("the session exposes the token's login", async () => {
  const session = await authOptions.callbacks.session({
    session: { user: { email: "octocat@example.com", image: "https://avatars.example/1" } },
    token: { userId: "usr_00000000000000000000000000000001", login: "octocat" },
  });

  expect(session.user.login).toBe("octocat");
  expect(session.user.id).toBe("usr_00000000000000000000000000000001");
  expect(viewerName(viewerOf(session))).toBe("octocat");
});

test("a token without a login cannot become a viewer", async () => {
  const session = await authOptions.callbacks.session({
    session: { user: { email: "octocat@example.com" } },
    token: { userId: "usr_00000000000000000000000000000001" },
  });

  expect(session.user.login).toBeNull();
  expect(() => viewerOf(session)).toThrow("viewer session requires a login");
});

test("an existing browser JWT follows its recorded merge without another OAuth login", async () => {
  const oldID = "usr_00000000000000000000000000000002";
  sessionIdentity = { id: "usr_00000000000000000000000000000001", email: "canonical@example.com" };
  const token = await authOptions.callbacks.jwt({ token: { userId: oldID, login: "octocat", email: "old@example.com" } });
  expect(sessionSubjects.at(-1)).toBe(oldID);
  expect(token).toMatchObject({ userId: sessionIdentity.id, email: sessionIdentity.email, login: "octocat" });
  const session = await authOptions.callbacks.session({ session: { user: { email: token.email } }, token });
  expect(session.user.id).toBe(sessionIdentity.id);
});

test("a removed or suspended subject cannot remain signed in", async () => {
  sessionIdentity = null;
  const token = await authOptions.callbacks.jwt({ token: { userId: "usr_00000000000000000000000000000002", login: "octocat" } });
  expect(token.userId).toBeUndefined();
  const session = await authOptions.callbacks.session({ session: { user: { id: "stale", email: "old@example.com" } }, token });
  expect(session.user).toBeUndefined();
});

test("a session lookup outage is not silently treated as a missing account", async () => {
  sessionIdentity = new Error("database unavailable");
  const token = await authOptions.callbacks.jwt({ token: { userId: "usr_00000000000000000000000000000002", login: "octocat" } });
  // Throwing here would make next-auth clear the session cookie.
  expect(token).toMatchObject({ userId: "usr_00000000000000000000000000000002", login: "octocat" });
  const session = await authOptions.callbacks.session({ session: { user: { id: token.userId, email: "admin@example.com" } }, token });
  expect(session.user).toBeUndefined();
  // Keep the cookie recoverable, but never expose stale authorization on this request.
  sessionIdentity = { id: token.userId, email: "admin@example.com" };
  const refreshed = await authOptions.callbacks.jwt({ token });
  expect(refreshed.identityUnavailable).toBeUndefined();
  const recovered = await authOptions.callbacks.session({ session: { user: { email: refreshed.email } }, token: refreshed });
  expect(recovered.user.id).toBe(sessionIdentity.id);
  expect(recovered.user.login).toBe("octocat");
});
