import { createHash, randomBytes, randomUUID } from "node:crypto";

import { recipientHash } from "../email/preferences";
import { ControlError } from "../domain/errors";
import { assertResourceID } from "../domain/ids";
import { normalizeEmail } from "../domain/model";
import { lockOwnedApp } from "./app-access";
import { withTransaction, type SQLPool } from "./database";
import type { AppCollaborator } from "./product-repository";

export interface AppInvitation {
  id: string;
  email: string;
  delivery: "sending" | "sent" | "failed" | "delivered" | "delayed" | "bounced" | "complained" | "suppressed";
  createdAt: Date;
  expiresAt: Date;
}
export interface PreparedInvitation {
  invitation: AppInvitation;
  token: string;
  appSlug: string;
  inviterEmail: string;
}
export type AddAdminResult = { items: AppCollaborator[] } | PreparedInvitation;

export class InvitationError extends Error {
  constructor(public readonly code: "invalid_invitation" | "email_mismatch" | "already_invited" | "invite_rate_limited" | "invitation_suppressed") {
    super(code);
  }
}

export function invitationTokenHash(token: string): string {
  if (!/^[0-9a-f]{64}$/.test(token)) throw new InvitationError("invalid_invitation");
  return createHash("sha256").update(token).digest("hex");
}

const selection = `id, email, delivery, created_at AS "createdAt", expires_at AS "expiresAt"`;
type InvitationRow = AppInvitation & Record<string, unknown>;

export class InvitationRepository {
  constructor(private readonly database: SQLPool, private readonly now: () => Date = () => new Date()) {}

  async list(ownerID: string, appID: string): Promise<AppInvitation[]> {
    assertResourceID("user", ownerID);
    assertResourceID("app", appID);
    // One plain statement: the owner check and the rows share a snapshot, and
    // no lock, so a page render never queues behind a send or an acceptance.
    const result = await this.database.query<{ status: string } & { [K in keyof AppInvitation]: AppInvitation[K] | null }>(`SELECT u.status, i.id, i.email, i.delivery, i.created_at AS "createdAt", i.expires_at AS "expiresAt"
FROM cantelop_control.apps a
JOIN cantelop_control.users u ON u.id = a.owner_user_id
LEFT JOIN cantelop_control.app_invitations i ON i.app_id = a.id AND i.state = 'pending'
WHERE a.id = $1 AND a.owner_user_id = $2 AND a.status <> 'archived'
ORDER BY i.created_at, i.id`, [appID, ownerID]);
    const first = result.rows[0];
    if (!first) throw new ControlError("not_found");
    if (first.status !== "active") throw new ControlError("user_suspended");
    return result.rows.flatMap(({ id, email, delivery, createdAt, expiresAt }) =>
      id === null ? [] : [{ id, email: email!, delivery: delivery!, createdAt: createdAt!, expiresAt: expiresAt! }]);
  }

  async add(ownerID: string, appID: string, rawEmail: string): Promise<AddAdminResult> {
    const email = normalizeEmail(rawEmail.trim());
    if (email.length > 320 || !/^[^\s@<>\x00-\x1f\x7f]+@[^\s@<>\x00-\x1f\x7f]+\.[^\s@<>\x00-\x1f\x7f]+$/.test(email)) {
      throw new TypeError("invalid email");
    }
    return this.prepare(ownerID, appID, email);
  }

  async resend(ownerID: string, appID: string, invitationID: string): Promise<PreparedInvitation> {
    const result = await this.prepare(ownerID, appID, undefined, invitationID);
    if ("items" in result) throw new Error("resend cannot directly grant access");
    return result;
  }

  private async prepare(ownerID: string, appID: string, email?: string, invitationID?: string): Promise<AddAdminResult> {
    assertResourceID("user", ownerID);
    assertResourceID("app", appID);
    return withTransaction(this.database, async (client) => {
      // Serialize the per-owner send budget across apps, then all membership
      // mutations for this app. No network calls take place under these locks.
      await client.query("SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", [`invitations:${ownerID}`]);
      const app = await lockOwnedApp(client, appID, ownerID, "update_app_share_owner");
      const at = this.now();
      if (invitationID !== undefined) {
        const previous = await client.query<InvitationRow>(`SELECT ${selection} FROM cantelop_control.app_invitations
WHERE id = $1 AND app_id = $2 AND state = 'pending'`, [invitationID, appID]);
        const invitation = previous.rows[0];
        if (!invitation) throw new ControlError("not_found");
        if (at.getTime() - invitation.createdAt.getTime() < 60_000) throw new InvitationError("invite_rate_limited");
        email = invitation.email;
      } else {
        const users = await client.query<{ id: string; email: string; status: string }>(`SELECT id, email, status
FROM cantelop_control.users WHERE email = $1 FOR SHARE`, [email]);
        const user = users.rows[0];
        if (user) {
          if (user.status !== "active" || user.id === ownerID) throw new ControlError("conflict");
          const added = await client.query(`INSERT INTO cantelop_control.app_collaborators
(app_id, user_id, role, added_by_user_id, created_at) VALUES ($1, $2, 'admin', $3, $4)
ON CONFLICT (app_id, user_id) DO NOTHING`, [appID, user.id, ownerID, at]);
          if (added.rowCount !== 1) throw new ControlError("conflict");
          // An old pending link must not restore access after a later removal.
          await client.query(`UPDATE cantelop_control.app_invitations SET state = 'revoked'
WHERE app_id = $1 AND email = $2 AND state = 'pending'`, [appID, email]);
          return { items: [{ userID: user.id, email: user.email, role: "admin", createdAt: at }] };
        }
        const pending = await client.query(`SELECT 1 FROM cantelop_control.app_invitations
WHERE app_id = $1 AND email = $2 AND state = 'pending'`, [appID, email]);
        if (pending.rowCount) throw new InvitationError("already_invited");
      }
      // Serialize budgets across owners, so multiple apps cannot flood a recipient.
      await client.query("SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", [`invitation-recipient:${recipientHash(email!)}`]);
      const suppressed = await client.query("SELECT 1 FROM cantelop_control.invitation_suppressions WHERE recipient_hash = $1", [recipientHash(email!)]);
      if (suppressed.rowCount) throw new InvitationError("invitation_suppressed");
      const recipientBudget = await client.query<{ count: string; latest: Date | null }>(`SELECT count(*)::text AS count, max(created_at) AS latest
FROM cantelop_control.app_invitations WHERE email = $1 AND created_at > $2`, [email, new Date(at.getTime() - 86_400_000)]);
      if (Number(recipientBudget.rows[0].count) >= 3 || (recipientBudget.rows[0].latest && at.getTime() - recipientBudget.rows[0].latest.getTime() < 60_000)) {
        throw new InvitationError("invite_rate_limited");
      }
      const budget = await client.query<{ count: string }>(`SELECT count(*)::text AS count FROM cantelop_control.app_invitations
WHERE invited_by_user_id = $1 AND created_at > $2`, [ownerID, new Date(at.getTime() - 3_600_000)]);
      if (Number(budget.rows[0].count) >= 20) throw new InvitationError("invite_rate_limited");
      if (invitationID !== undefined) {
        await client.query("UPDATE cantelop_control.app_invitations SET state = 'revoked' WHERE id = $1", [invitationID]);
      }
      const token = randomBytes(32).toString("hex");
      const expiresAt = new Date(at.getTime() + 7 * 86_400_000);
      const created = await client.query<InvitationRow>(`INSERT INTO cantelop_control.app_invitations
(id, app_id, email, invited_by_user_id, token_hash, state, delivery, created_at, expires_at)
VALUES ($1, $2, $3, $4, $5, 'pending', 'sending', $6, $7) RETURNING ${selection}`,
      [randomUUID(), appID, email, ownerID, invitationTokenHash(token), at, expiresAt]);
      return { invitation: created.rows[0], token, appSlug: app.slug, inviterEmail: app.ownerEmail };
    });
  }

  async isSuppressed(email: string): Promise<boolean> {
    return Boolean((await this.database.query("SELECT 1 FROM cantelop_control.invitation_suppressions WHERE recipient_hash = $1", [recipientHash(email)])).rowCount);
  }

  async suppress(hash: string, reason: "opt_out" | "bounced" | "complained" | "suppressed" = "opt_out"): Promise<void> {
    if (!/^[a-f0-9]{64}$/.test(hash)) throw new TypeError("Invalid recipient hash");
    await this.database.query(`INSERT INTO cantelop_control.invitation_suppressions (recipient_hash, reason, created_at)
VALUES ($1, $2, $3) ON CONFLICT (recipient_hash) DO NOTHING`, [hash, reason, this.now()]);
  }

  async recordDelivery(id: string, delivery: "sent" | "failed" | "suppressed", providerID?: string): Promise<AppInvitation> {
    const result = await this.database.query<InvitationRow>(`UPDATE cantelop_control.app_invitations
SET delivery = CASE WHEN delivery = 'sending' THEN $2 ELSE delivery END,
provider_message_id = COALESCE(provider_message_id, $3)
WHERE id = $1 AND (provider_message_id IS NULL OR $3::text IS NULL OR provider_message_id = $3)
RETURNING ${selection}`, [id, delivery, providerID ?? null]);
    if (!result.rows[0]) throw new Error("Invitation receipt could not be recorded");
    return result.rows[0];
  }

  async recordEvent(event: { id: string; invitationID?: string; providerID: string; email: string; delivery: AppInvitation["delivery"]; createdAt: Date }): Promise<void> {
    await withTransaction(this.database, async (client) => {
      // The signed invitation tag correlates callbacks arriving before the send
      // response. Provider id + recipient prevent unrelated messages binding.
      const result = await client.query<InvitationRow>(`SELECT ${selection} FROM cantelop_control.app_invitations
WHERE (provider_message_id = $1 OR (id = $2 AND provider_message_id IS NULL)) AND email = $3 FOR UPDATE`,
      [event.providerID, event.invitationID ?? null, normalizeEmail(event.email)]);
      const row = result.rows[0];
      if (!row) return;
      const inserted = await client.query(`INSERT INTO cantelop_control.invitation_delivery_events (id, invitation_id, type, created_at)
VALUES ($1, $2, $3, $4) ON CONFLICT (id) DO NOTHING`, [event.id, row.id, event.delivery, event.createdAt]);
      if (!inserted.rowCount) return;
      const rank: Record<AppInvitation["delivery"], number> = { sending: 0, sent: 1, delayed: 2, delivered: 3, failed: 4, suppressed: 5, bounced: 6, complained: 7 };
      await client.query(`UPDATE cantelop_control.app_invitations SET provider_message_id = $2, delivery = $3 WHERE id = $1`,
        [row.id, event.providerID, rank[event.delivery] > rank[row.delivery] ? event.delivery : row.delivery]);
      if (["bounced", "complained", "suppressed"].includes(event.delivery)) {
        await client.query(`INSERT INTO cantelop_control.invitation_suppressions (recipient_hash, reason, created_at)
VALUES ($1, $2, $3) ON CONFLICT (recipient_hash) DO NOTHING`, [recipientHash(row.email), event.delivery, this.now()]);
      }
    });
  }

  async revoke(ownerID: string, appID: string, invitationID: string): Promise<void> {
    assertResourceID("user", ownerID);
    assertResourceID("app", appID);
    await withTransaction(this.database, async (client) => {
      await lockOwnedApp(client, appID, ownerID, "update_app_share_owner");
      const result = await client.query(`UPDATE cantelop_control.app_invitations SET state = 'revoked'
WHERE id = $1 AND app_id = $2 AND state = 'pending'`, [invitationID, appID]);
      if (result.rowCount !== 1) throw new ControlError("not_found");
    });
  }

  async accept(userID: string, verifiedEmail: string, token: string): Promise<string> {
    assertResourceID("user", userID);
    const hash = invitationTokenHash(token);
    return withTransaction(this.database, async (client) => {
      // Lock the app before its invitation, just like send/revoke/archive.
      const app = await client.query<{ id: string; owner_user_id: string }>(`SELECT a.id, a.owner_user_id FROM cantelop_control.apps a
JOIN cantelop_control.app_invitations i ON i.app_id = a.id
WHERE i.token_hash = $1 AND a.status <> 'archived' FOR UPDATE OF a`, [hash]);
      if (!app.rows[0]) throw new InvitationError("invalid_invitation");
      const result = await client.query<{ id: string; email: string; invited_by_user_id: string; expires_at: Date; state: string }>(`SELECT id, email, invited_by_user_id, expires_at, state
FROM cantelop_control.app_invitations WHERE token_hash = $1 FOR UPDATE`, [hash]);
      const invitation = result.rows[0];
      if (!invitation || invitation.state !== "pending" || invitation.expires_at <= this.now() ||
          invitation.invited_by_user_id !== app.rows[0].owner_user_id) throw new InvitationError("invalid_invitation");
      const actor = await client.query<{ email: string; status: string }>(`SELECT email, status FROM cantelop_control.users WHERE id = $1 FOR SHARE`, [userID]);
      if (!actor.rows[0] || actor.rows[0].status !== "active") throw new ControlError("user_suspended");
      // Check both the current account and the verified OAuth session. An old
      // session or another linked identity's changed email must not claim it.
      if (actor.rows[0].email !== invitation.email || normalizeEmail(verifiedEmail) !== invitation.email) {
        throw new InvitationError("email_mismatch");
      }
      if (userID !== app.rows[0].owner_user_id) {
        await client.query(`INSERT INTO cantelop_control.app_collaborators
(app_id, user_id, role, added_by_user_id, created_at) VALUES ($1, $2, 'admin', $3, $4)
ON CONFLICT (app_id, user_id) DO NOTHING`, [app.rows[0].id, userID, invitation.invited_by_user_id, this.now()]);
      }
      await client.query(`UPDATE cantelop_control.app_invitations SET state = 'accepted', accepted_by_user_id = $2 WHERE id = $1`, [invitation.id, userID]);
      return app.rows[0].id;
    });
  }
}
