import type { Session } from "next-auth";

export type ViewerSession = Session & {
  user: NonNullable<Session["user"]> & { id: string; login: string };
};

/** The person the console is showing itself to. `login` and `avatarUrl` come
 *  from the identity provider — GitHub's login, or the local part of a Google
 *  address; `email` is contact metadata, never what the account is keyed by. */
export interface Viewer {
  login: string;
  email: string | null;
  avatarUrl: string | null;
}

/** The name the console addresses the viewer by: the provider login. */
export function viewerName(viewer: Viewer): string {
  return viewer.login;
}

export function viewerOf(session: ViewerSession): Viewer {
  if (!session.user.login) throw new Error("viewer session requires a login");
  return {
    login: session.user.login,
    email: session.user.email ?? null,
    avatarUrl: session.user.image ?? null,
  };
}
