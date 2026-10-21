import "server-only";

import { getServerSession } from "next-auth";
import { cache } from "react";

import { verifiedEmailAuthOptions } from "@/lib/auth";
import { viewerSession } from "@/lib/user-session";
import type { ViewerSession } from "@/lib/viewer";

/** The session with a provider-confirmed verifiedEmail, for the readers that
 *  authorize by address. It may ask the provider, so the rest use getUserSession. */
export const getVerifiedUserSession = cache(async (): Promise<ViewerSession | null> => {
  return viewerSession(await getServerSession(verifiedEmailAuthOptions));
});
