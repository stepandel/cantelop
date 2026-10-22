import type { SQLPool } from "../db/database";
import { FleetRepository } from "./repository";
import { FleetError, integer } from "./model";

/** All credit arithmetic is integral and transactional. Expiry alone never releases a reservation. */
export class FleetBudget {
  private repo: FleetRepository;
  constructor(private db: SQLPool) {
    this.repo = new FleetRepository(db);
  }
  async grant(app: string, epoch: string, included: number, expiresAt: string) {
    integer(included, 0, Number.MAX_SAFE_INTEGER);
    if (
      !/^[A-Za-z0-9_.:-]{1,128}$/.test(epoch) ||
      !Number.isFinite(Date.parse(expiresAt)) ||
      Date.parse(expiresAt) <= Date.now()
    )
      throw new FleetError("invalid_budget");
    await this.db.query(
      `INSERT INTO cantelop_control.fleet_budgets(app_id,epoch,included,expires_at) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`,
      [app, epoch, included, expiresAt],
    );
    const saved = (
      await this.db.query(`SELECT * FROM cantelop_control.fleet_budgets WHERE app_id=$1 AND epoch=$2`, [
        app,
        epoch,
      ])
    ).rows[0]!;
    if (
      BigInt(String(saved.included)) !== BigInt(included) ||
      date(saved.expires_at) !== Date.parse(expiresAt)
    )
      throw new FleetError("budget_grant_conflict", 409);
    return this.read(app, epoch);
  }
  async read(app: string, epoch: string) {
    const row = (
      await this.db.query(
        `SELECT app_id,epoch,included::text,settled::text,reserved::text,(included-settled-reserved)::text AS available,expires_at
      FROM cantelop_control.fleet_budgets WHERE app_id=$1 AND epoch=$2`,
        [app, epoch],
      )
    ).rows[0];
    if (!row) throw new FleetError("not_found", 404);
    return row;
  }
  /** Every epoch granted to one App, latest expiry first. */
  async listForApp(app: string) {
    return (
      await this.db.query(
        `SELECT epoch,included::text,settled::text,reserved::text,(included-settled-reserved)::text AS available,expires_at
      FROM cantelop_control.fleet_budgets WHERE app_id=$1 ORDER BY expires_at DESC,epoch`,
        [app],
      )
    ).rows;
  }
  async reserve(app: string, epoch: string, host: string, id: string, credits: number, expiresAt: string) {
    integer(credits, 1, Number.MAX_SAFE_INTEGER);
    if (
      !/^[A-Za-z0-9_.:-]{16,128}$/.test(id) ||
      !/^[A-Za-z0-9_.:-]{1,128}$/.test(host) ||
      Date.parse(expiresAt) <= Date.now() ||
      Date.parse(expiresAt) > Date.now() + 60_000 ||
      !Number.isFinite(Date.parse(expiresAt))
    )
      throw new FleetError("invalid_reservation");
    return this.repo.transaction(async (tx) => {
      await tx.query(`SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, [id]);
      const old = (await tx.query(`SELECT * FROM cantelop_control.fleet_reservations WHERE id=$1`, [id]))
        .rows[0];
      if (old) {
        if (
          old.app_id !== app ||
          old.epoch !== epoch ||
          old.host_id !== host ||
          BigInt(String(old.credits)) !== BigInt(credits) ||
          date(old.expires_at) !== Date.parse(expiresAt)
        )
          throw new FleetError("reservation_conflict", 409);
        return old;
      }
      const changed = await tx.query(
        `UPDATE cantelop_control.fleet_budgets SET reserved=reserved+$3
        WHERE app_id=$1 AND epoch=$2 AND included-settled-reserved>=$3 AND expires_at>=$4 AND expires_at>now() RETURNING app_id`,
        [app, epoch, credits, expiresAt],
      );
      if (!changed.rowCount) throw new FleetError("usage_limit_reached", 429);
      return (
        await tx.query(
          `INSERT INTO cantelop_control.fleet_reservations(id,app_id,epoch,host_id,credits,expires_at) VALUES($1,$2,$3,$4,$5,$6) RETURNING *`,
          [id, app, epoch, host, credits, expiresAt],
        )
      ).rows[0]!;
    });
  }
  /** Called only with authenticated, final host evidence. A conflicting settlement cannot refill credit. */
  async settle(id: string, host: string, consumed: number) {
    integer(consumed, 0, Number.MAX_SAFE_INTEGER);
    return this.repo.transaction(async (tx) => {
      const row = (
        await tx.query(
          `SELECT * FROM cantelop_control.fleet_reservations WHERE id=$1 AND host_id=$2 FOR UPDATE`,
          [id, host],
        )
      ).rows[0];
      if (!row) throw new FleetError("not_found", 404);
      if (BigInt(consumed) > BigInt(String(row.credits))) throw new FleetError("reservation_overrun", 409);
      if (row.consumed !== null) {
        if (BigInt(String(row.consumed)) !== BigInt(consumed))
          throw new FleetError("settlement_conflict", 409);
        return;
      }
      await tx.query(
        `UPDATE cantelop_control.fleet_budgets SET reserved=reserved-$3,settled=settled+$4 WHERE app_id=$1 AND epoch=$2`,
        [row.app_id, row.epoch, row.credits, consumed],
      );
      await tx.query(`UPDATE cantelop_control.fleet_reservations SET consumed=$2 WHERE id=$1`, [
        id,
        consumed,
      ]);
      await tx.query(
        `INSERT INTO cantelop_control.fleet_usage(id,fleet_id,app_id,reservation_id,credits)
        SELECT $1,fleet_id,app_id,$2,$3 FROM cantelop_control.fleet_installations WHERE app_id=$4`,
        [`usage:${id}`, id, consumed, row.app_id],
      );
    });
  }
}
function date(value: unknown) {
  return value instanceof Date ? value.getTime() : Date.parse(String(value));
}
