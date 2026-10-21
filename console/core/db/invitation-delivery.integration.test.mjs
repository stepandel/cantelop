import { expect, test } from "bun:test";
import { randomUUID } from "node:crypto";
import { fileURLToPath } from "node:url";
import { drizzle } from "drizzle-orm/node-postgres";
import { migrate } from "drizzle-orm/node-postgres/migrator";
import pg from "pg";
import { InvitationRepository } from "./invitation-repository";
import { purgeExpiredInvitations } from "../email/maintenance";
import { recipientHash } from "../email/preferences";
import { newResourceID } from "../domain/ids";
const dbTest = process.env.CANTELOP_TEST_DATABASE_URL ? test : test.skip;
dbTest("callbacks before receipts, replay, out-of-order delivery, suppression and cross-owner recipient budgets", async () => {
  const pool = new pg.Pool({ connectionString: process.env.CANTELOP_TEST_DATABASE_URL });
  const users = [newResourceID("user"), newResourceID("user")];
  const apps = [newResourceID("app"), newResourceID("app")];
  const suffix = randomUUID(); const email = `delivery-${suffix}@example.com`;
  let at = new Date(); const repo = new InvitationRepository(pool, () => at);
  try {
    await migrate(drizzle(pool), { migrationsFolder: fileURLToPath(new URL("./drizzle", import.meta.url)), migrationsTable: "__drizzle_migrations", migrationsSchema: "cantelop_control" });
    for (let i = 0; i < 2; i++) {
      await pool.query("INSERT INTO cantelop_control.users (id,email,status,created_at,last_login_at) VALUES ($1,$2,'active',$3,$3)", [users[i], `owner-${i}-${suffix}@example.com`, at]);
      await pool.query("INSERT INTO cantelop_control.apps (id,owner_user_id,slug,status,created_at,updated_at) VALUES ($1,$2,$3,'active',$4,$4)", [apps[i], users[i], `delivery-${i}-${suffix}`, at]);
    }
    const prepared = await repo.add(users[0], apps[0], email);
    const event = { id: `event-${suffix}`, invitationID: prepared.invitation.id, providerID: `provider-${suffix}`, email, delivery: "delivered", createdAt: at };
    await repo.recordEvent({ ...event, email: "someone-else@example.com" });
    expect((await repo.list(users[0], apps[0]))[0].delivery).toBe("sending");
    await repo.recordEvent(event);
    expect((await repo.recordDelivery(prepared.invitation.id, "sent", event.providerID)).delivery).toBe("delivered");
    await repo.recordEvent(event);
    expect((await pool.query("SELECT count(*)::int AS count FROM cantelop_control.invitation_delivery_events WHERE invitation_id=$1", [prepared.invitation.id])).rows[0].count).toBe(1);
    await repo.recordEvent({ ...event, id: `late-${suffix}`, delivery: "sent" });
    expect((await repo.list(users[0], apps[0]))[0].delivery).toBe("delivered");
    await repo.recordEvent({ ...event, id: `complaint-${suffix}`, delivery: "complained" });
    await repo.recordEvent({ ...event, id: `later-${suffix}`, delivery: "delivered" });
    expect((await repo.list(users[0], apps[0]))[0].delivery).toBe("complained");
    expect(await repo.isSuppressed(email.toUpperCase())).toBe(true);
    at = new Date(at.getTime() + 61_000);
    await expect(repo.resend(users[0], apps[0], prepared.invitation.id)).rejects.toMatchObject({ code: "invitation_suppressed" });
    await expect(repo.add(users[1], apps[1], email)).rejects.toMatchObject({ code: "invitation_suppressed" });
    const limited = `limited-${suffix}@example.com`;
    const first = await repo.add(users[0], apps[0], limited);
    await expect(repo.add(users[1], apps[1], limited)).rejects.toMatchObject({ code: "invite_rate_limited" });
    at = new Date(at.getTime() + 61_000);
    await repo.add(users[1], apps[1], limited);
    at = new Date(at.getTime() + 61_000);
    const third = await repo.resend(users[0], apps[0], first.invitation.id);
    at = new Date(at.getTime() + 61_000);
    await expect(repo.resend(users[0], apps[0], third.invitation.id)).rejects.toMatchObject({ code: "invite_rate_limited" });
    // Opt-out survives revocation/deletion of the original invitation.
    await repo.suppress(recipientHash(limited));
    await repo.revoke(users[0], apps[0], third.invitation.id);
    expect(await repo.isSuppressed(limited)).toBe(true);
    await pool.query("UPDATE cantelop_control.app_invitations SET created_at=$2, expires_at=$3 WHERE id=$1", [prepared.invitation.id, new Date(at.getTime() - 100 * 86400000), new Date(at.getTime() - 92 * 86400000)]);
    expect(await purgeExpiredInvitations(pool, at)).toBe(1);
    expect(await repo.isSuppressed(email)).toBe(true);
    expect((await pool.query("SELECT count(*)::int AS count FROM cantelop_control.invitation_delivery_events WHERE invitation_id=$1", [prepared.invitation.id])).rows[0].count).toBe(0);
    await pool.query("DELETE FROM cantelop_control.invitation_suppressions WHERE recipient_hash=$1", [recipientHash(limited)]);
  } finally {
    await pool.query("DELETE FROM cantelop_control.invitation_suppressions WHERE recipient_hash=$1", [recipientHash(email)]);
    await pool.query("DELETE FROM cantelop_control.apps WHERE id=ANY($1)", [apps]);
    await pool.query("DELETE FROM cantelop_control.users WHERE id=ANY($1)", [users]);
    await pool.end();
  }
}, 30_000);
