import type { DefaultSession } from "next-auth";

declare module "next-auth" {
  interface Session {
    user?: DefaultSession["user"] & { id: string; login?: string | null; verifiedEmail?: string | null };
  }

  /** The provider login travels with the user so the console can address the
   *  person by a name they know: GitHub's login, or for Google, which has no
   *  handle, the local part of their address. */
  interface User {
    login?: string | null;
  }
}

declare module "next-auth/jwt" {
  interface JWT {
    userId?: string;
    identityUnavailable?: boolean;
    login?: string | null;
    verifiedEmail?: string | null;
    identityProvider?: string;
    providerSubject?: string;
    providerAccessToken?: string;
    providerAccessTokenExpiresAt?: number;
    providerRefreshToken?: string;
  }
}
