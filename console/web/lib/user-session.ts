import "server-only";

import { getServerSession, type Session } from "next-auth";
import { cache } from "react";

import { authOptions } from "@/lib/auth";
import type { ViewerSession } from "@/lib/viewer";

// Request-memoized: a layout and the page under it each guard on the session,
// and every read refreshes the identity from the database.
export const getUserSession = cache(async (): Promise<ViewerSession | null> => {
  return viewerSession(await getServerSession(authOptions));
});

export function viewerSession(session: Session | null): ViewerSession | null {
  return session?.user?.id && session.user.login ? session as ViewerSession : null;
}
