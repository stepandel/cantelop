import "server-only";

import { getServerSession } from "next-auth";
import { cache } from "react";

import { verifiedEmailAuthOptions } from "@/lib/auth";
import { isVerifiedAdminSession } from "@/lib/admin-identity";
import type { ViewerSession } from "@/lib/viewer";

// Request-memoized, so the admin layout and its section page share one read.
export const getAdminSession = cache(async (): Promise<ViewerSession | null> => {
  const session = await getServerSession(verifiedEmailAuthOptions);
  return isVerifiedAdminSession(session, process.env.ADMIN_EMAIL) ? session : null;
});
