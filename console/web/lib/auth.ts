import type { NextAuthOptions } from "next-auth";
import GitHubProvider, { type GithubProfile } from "next-auth/providers/github";
import GoogleProvider, { type GoogleProfile } from "next-auth/providers/google";

import { resolveSessionIdentity, resolveUserIdentity } from "@core/auth/identity";
import { isIdentityProvider } from "@core/auth/providers";
import { currentVerifiedEmail, verifiedGithubEmail } from "@/lib/provider-email";

export { verifiedGithubEmail };

function required(name: "GITHUB_ID" | "GITHUB_SECRET" | "GOOGLE_ID" | "GOOGLE_SECRET") {
  const value = process.env[name];
  if (!value) throw new Error(`${name} is required`);
  return value;
}

/** Google has no handle. The console addresses people by a login, so a Google
 *  user gets the part of their address they type — the nearest thing they have
 *  to one, and it sits in the mono sidebar the way a GitHub login does. */
export function googleLogin(profile: Pick<GoogleProfile, "sub" | "email" | "name">): string {
  const local = profile.email?.split("@")[0]?.trim();
  return local || profile.name?.trim() || profile.sub;
}

type VerifiedGithubProfile = GithubProfile & { email_verified?: boolean };

type JwtCallback = NonNullable<NonNullable<NextAuthOptions["callbacks"]>["jwt"]>;

// The jwt callback runs on every session read. Asking the provider for the
// address again puts GitHub or Google on that read's latency path, and only
// email-based authorization (the admin view, accepting an invitation) needs
// it, so ordinary reads drop the claim and those readers opt in.
function refreshToken({ verifyEmail }: { verifyEmail: boolean }): JwtCallback {
  return async ({ token, account, user }) => {
    if (account && isIdentityProvider(account.provider)) {
      // signIn only admits provider-verified addresses, so a token without
      // one is a wiring fault; name it rather than fail on an empty string.
      if (!token.email) throw new Error(`${account.provider} sign-in reached the token without a verified address`);
      // Keep the provider-verified address separate from the product contact
      // address, which may remain unchanged after an email collision.
      token.verifiedEmail = token.email;
      token.identityProvider = account.provider;
      token.providerSubject = account.providerAccountId;
      token.providerAccessToken = account.access_token;
      token.providerAccessTokenExpiresAt = account.expires_at;
      token.providerRefreshToken = account.refresh_token;
      const identity = await resolveUserIdentity(account.provider, account.providerAccountId, token.email);
      token.userId = identity.id;
      token.login = user?.login ?? null;
      delete token.identityUnavailable;
    } else if (account) {
      delete token.verifiedEmail;
      delete token.identityProvider;
      delete token.providerSubject;
      delete token.providerAccessToken;
      delete token.providerAccessTokenExpiresAt;
      delete token.providerRefreshToken;
    } else if (token.userId) {
      // The cookie may still carry the address confirmed at sign-in; only a
      // fresh provider answer may stand as the claim.
      delete token.verifiedEmail;
      let identity;
      try { identity = await resolveSessionIdentity(token.userId); }
      catch (error) {
        // A transient lookup failure must not clear the session cookie; keep
        // the current subject and let the next refresh resolve it.
        console.error("session identity refresh failed", error);
        token.identityUnavailable = true;
        return token;
      }
      delete token.identityUnavailable;
      if (identity) {
        token.userId = identity.id;
        token.email = identity.email;
      } else {
        // Deleted pre-alias accounts and suspended users must sign in again.
        delete token.userId;
      }
      if (verifyEmail) {
        token.verifiedEmail = token.userId
          ? await currentVerifiedEmail({
            provider: token.identityProvider,
            subject: token.providerSubject,
            accessToken: token.providerAccessToken,
            accessTokenExpiresAt: token.providerAccessTokenExpiresAt,
            refreshToken: token.providerRefreshToken,
          })
          : null;
      }
    }
    return token;
  };
}

export const authOptions: NextAuthOptions = {
  providers: [
    GitHubProvider({
      clientId: required("GITHUB_ID"),
      clientSecret: required("GITHUB_SECRET"),
      userinfo: {
        url: "https://api.github.com/user",
        async request({ client, tokens }) {
          const profile = await client.userinfo<VerifiedGithubProfile>(tokens.access_token!);
          const response = await fetch("https://api.github.com/user/emails", {
            headers: {
              Accept: "application/vnd.github+json",
              Authorization: `Bearer ${tokens.access_token}`,
              "X-GitHub-Api-Version": "2022-11-28",
            },
          });
          if (!response.ok) return { ...profile, email: undefined, email_verified: false };
          const email = verifiedGithubEmail(await response.json());
          return { ...profile, email: email ?? undefined, email_verified: email !== null };
        },
      },
      // The console addresses people the way GitHub does — login and avatar —
      // so both have to survive the default profile mapping, which keeps only
      // name, email, and image.
      profile(profile: GithubProfile) {
        return {
          id: profile.id.toString(),
          login: profile.login,
          name: profile.name ?? profile.login,
          email: profile.email,
          image: profile.avatar_url,
        };
      },
    }),
    GoogleProvider({
      clientId: required("GOOGLE_ID"),
      clientSecret: required("GOOGLE_SECRET"),
      // Always offer the account chooser: someone accepting an invitation with
      // the wrong account has no other way to switch, since Google would
      // otherwise silently reuse the session it already has.
      // Offline access with consent is the only way Google issues a refresh
      // token on every sign-in, and email revalidation needs one to outlive
      // the hour-long access token.
      authorization: { params: { prompt: "consent select_account", access_type: "offline" } },
      profile(profile: GoogleProfile) {
        const login = googleLogin(profile);
        return {
          id: profile.sub,
          login,
          name: profile.name ?? login,
          email: profile.email,
          image: profile.picture,
        };
      },
    }),
  ],
  session: { strategy: "jwt" },
  // The home page is the front door: it carries the sign-in buttons and shows
  // OAuth errors. There are no dedicated /signin or /signout pages.
  pages: {
    signIn: "/",
    error: "/",
  },
  callbacks: {
    // A verified address is the account-linking key and ADMIN_EMAIL grants the
    // operator view by address. An unverified address must never reach either
    // boundary; the refusal lands as AccessDenied.
    async signIn({ account, profile }) {
      if (account?.provider === "google") {
        const google = profile as GoogleProfile | undefined;
        return google?.email_verified === true && Boolean(google.email);
      }
      if (account?.provider === "github") {
        const github = profile as VerifiedGithubProfile | undefined;
        return github?.email_verified === true && Boolean(github.email);
      }
      return false;
    },
    jwt: refreshToken({ verifyEmail: false }),
    async session({ session, token }) {
      if (!token.userId || token.identityUnavailable) {
        delete session.user;
        return session;
      }
      if (session.user) {
        session.user.id = token.userId;
        session.user.login = token.login ?? null;
        session.user.verifiedEmail = token.verifiedEmail ?? null;
      }
      return session;
    },
  },
};

// For the readers that authorize by address: the session carries a
// provider-confirmed verifiedEmail, or null when the provider cannot vouch.
export const verifiedEmailAuthOptions: NextAuthOptions = {
  ...authOptions,
  callbacks: { ...authOptions.callbacks, jwt: refreshToken({ verifyEmail: true }) },
};
