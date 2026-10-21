import { and, eq, or, type SQL } from "drizzle-orm";

import { ControlError } from "../domain/errors";
import { assertResourceID } from "../domain/ids";
import type { SQLClient, SQLPool } from "./database";
import { appCollaborators, apps } from "./schema";

/**
 * The one definition of who may operate an App: its owner or an admin
 * collaborator. Raw SQL callers alias the App row `a` and bind the actor to
 * `userParam`; drizzle callers LEFT JOIN with `appAccessJoin` and filter with
 * `appAccessible`.
 */
export function appAccessibleSQL(userParam = "$2"): string {
  return `(a.owner_user_id = ${userParam} OR EXISTS (
    SELECT 1 FROM cantelop_control.app_collaborators c
    WHERE c.app_id = a.id AND c.user_id = ${userParam} AND c.role = 'admin'
  ))`;
}

export function appAccessJoin(userID: string): SQL {
  return and(eq(appCollaborators.appId, apps.id), eq(appCollaborators.userId, userID))!;
}

export function appAccessible(userID: string): SQL {
  return or(eq(apps.ownerUserId, userID), eq(appCollaborators.role, "admin"))!;
}

export function appAccessRole(ownerUserID: string, userID: string): "owner" | "admin" {
  return ownerUserID === userID ? "owner" : "admin";
}

/**
 * Row locks lockOwnedApp takes on the App (`a`) and its owner (`u`). Callers
 * name their mode explicitly so a shared helper never changes lock ordering.
 */
export type OwnedAppLockMode = "update_app" | "update_app_share_owner" | "share_app_and_owner";

const ownedAppLockClauses: Record<OwnedAppLockMode, string> = {
  update_app: "FOR UPDATE OF a",
  update_app_share_owner: "FOR UPDATE OF a FOR SHARE OF u",
  share_app_and_owner: "FOR SHARE OF a, u",
};

/**
 * Locks an unarchived App held by `ownerID` and requires that owner to be
 * active: `not_found` for a missing, archived or foreign App, otherwise
 * `user_suspended` for a suspended owner.
 */
export async function lockOwnedApp(client: SQLClient, appID: string, ownerID: string,
  mode: OwnedAppLockMode): Promise<{ slug: string; ownerEmail: string }> {
  const result = await client.query<{ status: string; slug: string; email: string }>(`SELECT u.status, a.slug, u.email
FROM cantelop_control.apps a
JOIN cantelop_control.users u ON u.id = a.owner_user_id
WHERE a.id = $1 AND a.owner_user_id = $2 AND a.status <> 'archived'
${ownedAppLockClauses[mode]}`, [appID, ownerID]);
  const app = result.rows[0];
  if (result.rowCount !== 1 || !app) throw new ControlError("not_found");
  if (app.status !== "active") throw new ControlError("user_suspended");
  return { slug: app.slug, ownerEmail: app.email };
}

/** The owner whose tenant key durable operations and owned rows use, if the actor may operate the App. */
export async function resolveAppOwnerForUser(database: SQLPool, userID: string, appID: string): Promise<string> {
  assertResourceID("user", userID);
  assertResourceID("app", appID);
  const result = await database.query<{ owner_user_id: string; user_status: "active" | "suspended" }>(`SELECT a.owner_user_id, actor.status AS user_status
FROM cantelop_control.apps a
JOIN cantelop_control.users actor ON actor.id = $2
WHERE a.id = $1 AND a.status <> 'archived'
  AND ${appAccessibleSQL()}`, [appID, userID]);
  if (result.rowCount !== 1) throw new ControlError("not_found");
  if (result.rows[0].user_status !== "active") throw new ControlError("user_suspended");
  return result.rows[0].owner_user_id;
}
