import { expect, test } from "bun:test";
import { randomUUID } from "node:crypto";
import { fileURLToPath } from "node:url";
import { drizzle } from "drizzle-orm/node-postgres";
import { migrate } from "drizzle-orm/node-postgres/migrator";
import pg from "pg";
import { controlDatabaseConfig } from "./database";
import { InvitationRepository, invitationTokenHash } from "./invitation-repository";
import { ProductRepository } from "./product-repository";
import { newResourceID } from "../domain/ids";

const databaseURL = process.env.CANTELOP_TEST_DATABASE_URL;
const postgresTest = databaseURL ? test : test.skip;

postgresTest("invitations enforce lifecycle, identity, ownership, send budgets, and concurrent single use", async () => {
  const pool = new pg.Pool(controlDatabaseConfig({
    CANTELOP_DATABASE_URL: databaseURL,
    CANTELOP_DATABASE_SSL: process.env.CANTELOP_DATABASE_SSL ?? "disable",
  }));
  const suffix = randomUUID();
  const ownerID = newResourceID("user");
  const guestID = newResourceID("user");
  const otherID = newResourceID("user");
  const appID = newResourceID("app");
  let at = new Date();
  const repository = new InvitationRepository(pool, () => at);
  const guestEmail = `guest-${suffix}@example.com`;
  const seedUser = (id, email) => pool.query(`INSERT INTO cantelop_control.users
(id, email, status, created_at, last_login_at) VALUES ($1, $2, 'active', $3, $3)`, [id, email, at]);
  const rejects = (promise, code) => expect(promise).rejects.toMatchObject({ code });
  try {
    await migrate(drizzle(pool), { migrationsFolder: fileURLToPath(new URL("./drizzle", import.meta.url)), migrationsTable: "__drizzle_migrations", migrationsSchema: "cantelop_control" });
    await seedUser(ownerID, `owner-${suffix}@example.com`);
    await seedUser(otherID, `other-${suffix}@example.com`);
    await pool.query(`INSERT INTO cantelop_control.apps (id, owner_user_id, slug, status, created_at, updated_at)
VALUES ($1, $2, $3, 'active', $4, $4)`, [appID, ownerID, `invite-${suffix}`, at]);

    await rejects(repository.add(otherID, appID, guestEmail), "not_found");
    await rejects(repository.list(otherID, appID), "not_found");
    const invite = await repository.add(ownerID, appID, ` ${guestEmail.toUpperCase()} `);
    expect(invite.invitation.email).toBe(guestEmail);
    expect(invite.invitation.expiresAt.getTime() - at.getTime()).toBe(7 * 86_400_000);
    const stored = (await pool.query("SELECT * FROM cantelop_control.app_invitations WHERE id = $1", [invite.invitation.id])).rows[0];
    expect(stored.token_hash).toBe(invitationTokenHash(invite.token));
    expect(JSON.stringify(stored)).not.toContain(invite.token);
    expect((await pool.query("SELECT * FROM cantelop_control.app_collaborators WHERE app_id = $1", [appID])).rowCount).toBe(0);
    await rejects(repository.add(ownerID, appID, guestEmail), "already_invited");
    await rejects(repository.resend(ownerID, appID, invite.invitation.id), "invite_rate_limited");
    await rejects(repository.revoke(otherID, appID, invite.invitation.id), "not_found");
    await rejects(repository.resend(otherID, appID, invite.invitation.id), "not_found");
    await rejects(repository.accept(otherID, `other-${suffix}@example.com`, invite.token), "email_mismatch");
    await repository.recordDelivery(invite.invitation.id, "failed");
    expect((await repository.list(ownerID, appID))[0].delivery).toBe("failed");

    at = new Date(at.getTime() + 61_000);
    const resent = await repository.resend(ownerID, appID, invite.invitation.id);
    expect(resent.token).not.toBe(invite.token);
    await rejects(repository.accept(otherID, guestEmail, invite.token), "invalid_invitation");
    await seedUser(guestID, guestEmail);
    await rejects(repository.accept(guestID, "stale@example.com", resent.token), "email_mismatch");
    // Both OAuth providers resolve to the same user before redemption.
    const products = new ProductRepository(pool, () => at);
    expect((await products.resolveIdentity({ provider: "google", providerSubject: suffix, email: guestEmail })).id).toBe(guestID);
    expect((await products.resolveIdentity({ provider: "github", providerSubject: suffix, email: guestEmail })).id).toBe(guestID);
    const accepted = await Promise.allSettled([
      repository.accept(guestID, guestEmail, resent.token),
      repository.accept(guestID, guestEmail, resent.token),
    ]);
    expect(accepted.filter((result) => result.status === "fulfilled")).toHaveLength(1);
    expect(accepted.filter((result) => result.status === "rejected")[0].reason.code).toBe("invalid_invitation");
    expect((await pool.query("SELECT * FROM cantelop_control.app_collaborators WHERE app_id = $1", [appID])).rowCount).toBe(1);
    expect(await repository.list(ownerID, appID)).toEqual([]);
    await products.removeAppAdminForOwner(ownerID, appID, guestID);
    await rejects(repository.accept(guestID, guestEmail, resent.token), "invalid_invitation");
    expect((await repository.add(ownerID, appID, guestEmail)).items[0].userID).toBe(guestID);
    await rejects(repository.add(ownerID, appID, guestEmail), "conflict");

    const revoked = await repository.add(ownerID, appID, `revoked-${suffix}@example.com`);
    await repository.revoke(ownerID, appID, revoked.invitation.id);
    await rejects(repository.accept(guestID, guestEmail, revoked.token), "invalid_invitation");
    const expired = await repository.add(ownerID, appID, `expired-${suffix}@example.com`);
    at = new Date(at.getTime() + 7 * 86_400_000);
    await rejects(repository.accept(guestID, guestEmail, expired.token), "invalid_invitation");
    const renewed = await repository.resend(ownerID, appID, expired.invitation.id);
    expect(renewed.invitation.expiresAt > at).toBe(true);

    await pool.query("UPDATE cantelop_control.users SET status = 'suspended' WHERE id = $1", [ownerID]);
    await rejects(repository.add(ownerID, appID, `blocked-${suffix}@example.com`), "user_suspended");
    await pool.query("UPDATE cantelop_control.users SET status = 'active' WHERE id = $1", [ownerID]);
    await pool.query("UPDATE cantelop_control.users SET status = 'suspended' WHERE id = $1", [guestID]);
    await rejects(repository.accept(guestID, guestEmail, renewed.token), "user_suspended");
    await rejects(repository.add(ownerID, appID, guestEmail), "conflict");
    await pool.query("UPDATE cantelop_control.users SET status = 'active' WHERE id = $1", [guestID]);
    await pool.query("UPDATE cantelop_control.apps SET status = 'archived', archived_at = $2 WHERE id = $1", [appID, at]);
    await rejects(repository.accept(guestID, guestEmail, renewed.token), "invalid_invitation");
    await rejects(repository.resend(ownerID, appID, renewed.invitation.id), "not_found");
    await pool.query("UPDATE cantelop_control.apps SET status = 'active', archived_at = NULL WHERE id = $1", [appID]);

    // One send in this hour already; simultaneous sends cannot exceed 20.
    const sends = await Promise.allSettled(Array.from({ length: 21 }, (_, i) => repository.add(ownerID, appID, `budget-${i}-${suffix}@example.com`)));
    expect(sends.filter((result) => result.status === "fulfilled")).toHaveLength(19);
    expect(sends.filter((result) => result.status === "rejected").map((result) => result.reason.code)).toEqual(["invite_rate_limited", "invite_rate_limited"]);
  } finally {
    await pool.query("DELETE FROM cantelop_control.apps WHERE id = $1", [appID]);
    await pool.query("DELETE FROM cantelop_control.users WHERE id = ANY($1)", [[ownerID, guestID, otherID]]);
    await pool.end();
  }
}, 30_000);
