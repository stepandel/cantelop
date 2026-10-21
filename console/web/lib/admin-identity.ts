import type { Session } from "next-auth";

import type { ViewerSession } from "@/lib/viewer";

export function isVerifiedAdminSession(
  session: Session | null,
  configuredEmail: string | undefined,
): session is ViewerSession {
  const allowedEmail = configuredEmail?.trim().toLowerCase();
  const verifiedEmail = session?.user?.verifiedEmail?.trim().toLowerCase();
  return Boolean(allowedEmail && verifiedEmail === allowedEmail && session?.user?.id && session.user.login);
}
