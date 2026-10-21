import { NextRequest, NextResponse } from "next/server";
import { authenticatedUserID } from "@core/http/auth";
import { readControlJSON } from "@core/http/request";
import { productErrorResponse } from "@core/http/response";
import { configurationControl } from "@core/configuration/control";
import { fleetControl } from "@core/fleets/control";
import { FleetBudget } from "@core/fleets/budget";
import { FleetError, requireScope, identifier } from "@core/fleets/model";

export const dynamic = "force-dynamic";
type Context = { params: Promise<{ path?: string[] }> };
async function handle(request: NextRequest, context: Context) {
  const repo = fleetControl(),
    path = (await context.params).path ?? [],
    method = request.method;
  const authorization = request.headers.get("authorization");
  const body = () => readControlJSON(request, 16 * 1024) as Promise<Record<string, unknown>>;
  const json = (value: unknown, status = 200) =>
    NextResponse.json(value, { status, headers: { "Cache-Control": "private, no-store" } });
  try {
    if (path.length === 0 && method === "GET") {
      const owner = await authenticatedUserID(authorization, "tenant:write");
      return json({
        items: (
          await repo.db.query(
            `SELECT id,name,max_apps,created_at FROM cantelop_control.fleets WHERE owner_user_id=$1 ORDER BY created_at`,
            [owner],
          )
        ).rows,
      });
    }
    if (path.length === 0 && method === "POST") {
      const owner = await authenticatedUserID(authorization, "tenant:write"),
        input = await body();
      return json(await repo.create(owner, input.name, input.maxApps), 201);
    }
    const fleet = path[0]!;
    identifier(fleet, "flt");
    if (path.length === 1 && method === "PATCH") {
      const owner = await authenticatedUserID(authorization, "tenant:write"),
        input = await body();
      return json(await repo.updateCapacity(owner, fleet, input.maxApps));
    }
    if (path[1] === "principals") {
      const owner = await authenticatedUserID(authorization, "tenant:write");
      if (path.length === 2 && method === "GET") {
        return json({
          items: (
            await repo.db.query(
              `SELECT p.id,p.label,p.scopes,p.expires_at,p.revoked_at FROM cantelop_control.fleet_principals p JOIN cantelop_control.fleets f ON f.id=p.fleet_id WHERE f.id=$1 AND f.owner_user_id=$2`,
              [fleet, owner],
            )
          ).rows,
        });
      }
      if (path.length === 2 && method === "POST") {
        const b = await body();
        return json(
          await repo.principal(owner, fleet, { label: b.label, scopes: b.scopes, expiresAt: b.expiresAt }),
          201,
        );
      }
      if (path.length === 3 && method === "DELETE") {
        await repo.revoke(owner, fleet, path[2]!);
        return json({ revoked: true });
      }
      throw new FleetError("not_found", 404);
    }
    const actor = await repo.authenticate(authorization, fleet);
    if (path[1] === "packages") {
      if (path.length === 2 && method === "POST") {
        const b = await body();
        return json(await repo.publish(actor, b.releaseId, b.version), 201);
      }
      if (path.length === 3 && method === "GET") return json(await repo.package(actor, path[2]!));
      if (path.length === 3 && method === "DELETE") {
        requireScope(actor, "packages:write");
        const changed = await repo.db.query(
          `UPDATE cantelop_control.fleet_packages SET disabled_at=now() WHERE id=$1 AND fleet_id=$2 RETURNING id`,
          [path[2], fleet],
        );
        if (!changed.rowCount) throw new FleetError("not_found", 404);
        await repo.audit(repo.db, actor, "package.disable", path[2]!);
        return json({ disabled: true });
      }
    }
    if (path[1] === "usage" && path.length === 2 && method === "GET") {
      requireScope(actor, "usage:read");
      const after = request.nextUrl.searchParams.get("after") ?? "0";
      if (!/^\d{1,16}$/.test(after) || !Number.isSafeInteger(Number(after)))
        throw new FleetError("invalid_cursor");
      const result = await repo.db.query(
        `SELECT u.sequence::text,u.id,u.app_id,u.reservation_id,u.credits::text,u.observed_at,r.epoch
        FROM cantelop_control.fleet_usage u JOIN cantelop_control.fleet_reservations r ON r.id=u.reservation_id WHERE u.fleet_id=$1 AND u.sequence>$2 ORDER BY u.sequence LIMIT 500`,
        [fleet, after],
      );
      return json({ items: result.rows, after: result.rows.at(-1)?.sequence ?? after });
    }
    if (path[1] === "installations") {
      if (path.length === 2 && method === "DELETE") {
        await repo.deleteByKey(actor, request.nextUrl.searchParams.get("externalKey") ?? "");
        return json({ deleted: true }, 202);
      }

      if (path.length === 2 && method === "GET") {
        requireScope(actor, "installations:read");
        const key = request.nextUrl.searchParams.get("externalKey") ?? "";
        if (!/^[A-Za-z0-9_.:-]{16,128}$/.test(key)) throw new FleetError("invalid_installation_key");
        const found = (
          await repo.db.query(
            `SELECT id FROM cantelop_control.fleet_installations WHERE fleet_id=$1 AND external_key=$2`,
            [fleet, key],
          )
        ).rows[0];
        if (!found) throw new FleetError("not_found", 404);
        return json(await repo.installation(actor, String(found.id)));
      }

      if (path.length === 3 && method === "PUT") {
        const b = await body();
        return json(await repo.install(actor, path[2]!, b.packageId), 202);
      }
      if (path.length === 3 && method === "GET") return json(await repo.installation(actor, path[2]!));
      const target = await repo.installation(
        actor,
        path[2]!,
        path[3] === "credentials"
          ? "credentials:read"
          : path[3] === "budget" && method === "GET"
            ? "usage:read"
            : path[3] === "policy" || path[3] === "budget"
              ? "policy:write"
              : "installations:write",
      );
      if (path.length === 4 && path[3] === "credentials" && method === "GET") {
        const c = await configurationControl().integrationCredentialsForUser(
          actor.ownerUserId,
          target.app_id,
        );
        return json({
          appId: c.app_id,
          slug: c.slug,
          edgeUrl: c.edge_url,
          accessToken: c.token,
          runtimeId: target.runtime_id,
        });
      }
      if (path.length === 4 && path[3] === "policy" && method === "PUT") {
        const b = await body();
        return json(
          await repo.updatePolicy(actor, path[2]!, {
            expectedRevision: b.expectedRevision,
            policy: b.policy,
            sourceFence: b.sourceFence,
          }),
          202,
        );
      }
      if (path.length === 4 && path[3] === "budget" && method === "GET") {
        const epoch = request.nextUrl.searchParams.get("epoch") ?? "";
        if (!/^[A-Za-z0-9_.:-]{1,128}$/.test(epoch)) throw new FleetError("invalid_epoch");
        return json(await new FleetBudget(repo.db).read(target.app_id, epoch));
      }
      if (path.length === 4 && path[3] === "budget" && method === "PUT") {
        const b = await body();
        if (typeof b.epoch !== "string" || typeof b.included !== "number" || typeof b.expiresAt !== "string")
          throw new FleetError("invalid_budget");
        return json(await new FleetBudget(repo.db).grant(target.app_id, b.epoch, b.included, b.expiresAt));
      }
      if (path.length === 3 && method === "DELETE") {
        // Same lock and permanent tombstone as deletion by external key.
        await repo.deleteByKey(actor, target.external_key);
        return json({ id: target.id, status: "deleting" }, 202);
      }
      if (path.length === 4 && path[3] === "releases" && method === "POST") {
        const b = await body();
        identifier(b.packageId, "pkg");
        const pkg = await repo.package(actor, b.packageId, "installations:write");
        if (target.package_id === b.packageId && !target.next_package_id)
          return json({ id: target.id, status: target.status });
        if (pkg.disabled_at) throw new FleetError("package_disabled", 409);
        const result = await repo.db.query(
          `UPDATE cantelop_control.fleet_installations SET next_package_id=$3,error_code=NULL,lease_until=NULL,updated_at=now()
          WHERE id=$1 AND fleet_id=$2 AND package_id=$4 AND (next_package_id IS NULL OR next_package_id=$3) AND desired_state='ready' RETURNING id`,
          [target.id, fleet, b.packageId, b.expectedPackageId],
        );
        if (!result.rowCount) throw new FleetError("installation_conflict", 409);
        await repo.audit(repo.db, actor, "installation.upgrade", target.id);
        return json({ id: target.id, status: "updating" }, 202);
      }
    }
    throw new FleetError("not_found", 404);
  } catch (error) {
    if (error instanceof FleetError) return json({ error: error.code }, error.status);
    return productErrorResponse(error);
  }
}
export { handle as GET, handle as POST, handle as PUT, handle as PATCH, handle as DELETE };
