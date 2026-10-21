import type { SQLPool } from "../db/database";
import type { ComputeRegistrationProvider } from "../compute-registrations/provider";
import type { ConfigRepository } from "../configuration/repository";
import type { PreparedArtifactStore } from "../session-runtime-materializations/prepared-artifact-store";
import type { SessionRuntimeProvider } from "../session-runtime-materializations/provider";

interface AppRow extends Record<string, unknown> {
 managed?:boolean;
  id: string;
  owner_user_id: string;
  slug: string;
  status: "active" | "provisioning";
  active_release_id: string | null;
  internal_port: number | null;
  config_revision: string;
}
interface WorkspaceRow extends Record<string, unknown> {
  id: string;
  app_id: string;
  slug: string;
}
interface BuildRow extends Record<string, unknown> {
  app_id: string;
  release_id: string;
  operation_id: string;
  output: Record<string, unknown>;
}
interface ReplayBuild {
  releaseID: string;
  operationID: string;
  buildID: string;
  version: number;
  manifestKey: string;
  manifestDigest: string;
}
interface ReplayApp {
 managed?:boolean;
  id: string;
  ownerUserID: string;
  slug: string;
  internalPort: number;
  configRevision: number;
  builds: ReplayBuild[];
  activeBuildID: string | null;
  workspaces: Array<{ id: string; slug: string }>;
}

export async function planFreshHostReplay(database: SQLPool): Promise<ReplayApp[]> {
  const client = await database.connect();
  try {
    await client.query("BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY");
    const unfinished = await client.query<{ count: string }>(`SELECT count(*)::text AS count
FROM cantelop_control.operations WHERE status IN ('queued', 'running')`);
    if (unfinished.rows[0]?.count !== "0") throw new Error("stop the control worker and settle all operations before host replay");
    const pendingReleases = await client.query<{ count: string }>(`SELECT count(*)::text AS count
FROM cantelop_control.releases WHERE status IN ('queued', 'provisioning', 'cancelling')`);
    if (pendingReleases.rows[0]?.count !== "0") throw new Error("settle all releases before host replay");
    const apps = await client.query<AppRow>(`SELECT a.id, a.owner_user_id, a.slug, a.status,
       a.active_release_id, a.config_revision::text, runtime.internal_port, EXISTS(SELECT 1 FROM cantelop_control.fleet_policies fp WHERE fp.app_id=a.id) AS managed
FROM cantelop_control.apps a
LEFT JOIN cantelop_control.app_session_runtimes runtime ON runtime.app_id = a.id
WHERE a.status <> 'archived' ORDER BY a.id`);
    const workspaces = await client.query<WorkspaceRow>(`SELECT id, app_id, slug
FROM cantelop_control.workspaces WHERE archived_at IS NULL ORDER BY app_id, id`);
    const builds = await client.query<BuildRow>(`SELECT r.id AS release_id, r.app_id, component.operation_id, component.output
FROM cantelop_control.release_components component
JOIN cantelop_control.releases r ON r.id = component.release_id
JOIN cantelop_control.apps a ON a.id = r.app_id
WHERE component.kind = 'session_runtime' AND component.status = 'succeeded' AND a.status <> 'archived'
ORDER BY r.app_id, r.version`);
    await client.query("COMMIT");

    const buildsByApp = new Map<string, ReplayBuild[]>();
    for (const row of builds.rows) {
      const output = row.output;
      if (!output || typeof output !== "object" ||
          !/^sha256:[0-9a-f]{64}$/.test(String(output.build_id)) ||
          !Number.isSafeInteger(output.build_version) || (output.build_version as number) < 1 ||
          !/^prepared-images\/manifests\/[0-9a-f]{64}\.json$/.test(String(output.prepared_manifest_key)) ||
          output.prepared_manifest_digest !== `sha256:${String(output.prepared_manifest_key).slice(26, -5)}`) {
        throw new Error(`release ${row.release_id} has no replayable prepared Build`);
      }
    }
    for (const row of builds.rows) {
      const output = row.output;
      const appID = row.app_id;
      const list = buildsByApp.get(appID) ?? [];
      list.push({
        releaseID: row.release_id,
        operationID: row.operation_id,
        buildID: output.build_id as string,
        version: output.build_version as number,
        manifestKey: output.prepared_manifest_key as string,
        manifestDigest: output.prepared_manifest_digest as string,
      });
      buildsByApp.set(appID, list);
    }
    return apps.rows.map((app) => {
      if (app.status !== "active") throw new Error(`App ${app.id} is still provisioning`);
      if (!Number.isInteger(app.internal_port) || (app.internal_port as number) < 1 ||
          (app.internal_port as number) > 65535) throw new Error(`App ${app.id} has no valid Session runtime`);
      const history = buildsByApp.get(app.id) ?? [];
      history.sort((a, b) => a.version - b.version || a.operationID.localeCompare(b.operationID));
      const distinct = new Map<number, ReplayBuild>();
      for (const build of history) {
        const existing = distinct.get(build.version);
        if (existing && existing.buildID !== build.buildID) throw new Error(`App ${app.id} has conflicting Build versions`);
        if (!existing) distinct.set(build.version, build);
      }
      const ordered = [...distinct.values()];
      ordered.forEach((build, index) => {
        if (build.version !== index + 1) throw new Error(`App ${app.id} has a Build history gap`);
      });
      const activeBuild = app.active_release_id ? history.find((build) => build.releaseID === app.active_release_id) : undefined;
      if (app.active_release_id && !activeBuild) {
        throw new Error(`App ${app.id} has no replayable active release`);
      }
      // Rollback and later-failed releases leave newer Builds than the active
      // release; the host still holds them, so replay the whole history.
      const configRevision = Number(app.config_revision);
      if (!Number.isSafeInteger(configRevision) || configRevision < 0) throw new Error(`App ${app.id} has an invalid configuration revision`);
      return {
        id: app.id, ownerUserID: app.owner_user_id, slug: app.slug,
        ...(app.managed?{managed:true}:{}),internalPort: app.internal_port as number, configRevision, builds: ordered,
        activeBuildID: activeBuild?.buildID ?? null,
        workspaces: workspaces.rows.filter((workspace) => workspace.app_id === app.id)
          .map((workspace) => ({ id: workspace.id, slug: workspace.slug })),
      };
    });
  } catch (error) {
    await client.query("ROLLBACK").catch(() => {});
    throw error;
  } finally {
    client.release();
  }
}

export async function applyFreshHostReplay(
  plan: ReplayApp[], compute: ComputeRegistrationProvider,
  runtime: SessionRuntimeProvider, config: Pick<ConfigRepository, "projection">,
  artifacts: Pick<PreparedArtifactStore, "readManifest" | "signImageDownload">,
): Promise<void> {
  const manifests = new Map<string, Awaited<ReturnType<PreparedArtifactStore["readManifest"]>>>();
  for (const app of plan) {
    for (const build of app.builds) {
      const manifest = await artifacts.readManifest(build.manifestKey, build.manifestDigest);
      if (manifest.build_id !== build.buildID) throw new Error(`App ${app.id} prepared Build identity mismatch`);
      manifests.set(build.operationID, manifest);
    }
  }
  for (const app of plan) {
    await compute.registerApp({ ownerUserID: app.ownerUserID, appID: app.id, appSlug: app.slug, internalPort: app.internalPort, ...(app.managed?{managed:true}:{}) });
    const admission = await runtime.releaseAdmission(app.ownerUserID, app.id);
    const generation = admission.generation || `act_${"0".repeat(32)}`;
    await runtime.setReleaseAdmission(app.ownerUserID, app.id, generation, true);
    let selected = false;
    let targetInstalled = false;
    for (const build of app.builds) {
      const manifest = manifests.get(build.operationID)!;
      const imageURL = await artifacts.signImageDownload(manifest.image_key, 3600);
      let completed = false;
      for (let attempt = 0; attempt < 360; attempt++) {
        const result = await runtime.materialize({
          ownerUserID: app.ownerUserID, appID: app.id, commandID: build.operationID,
          imageURL, manifest,
        });
        if (result.status === "failed") throw new Error(`App ${app.id} Build replay failed: ${result.errorCode}`);
        if (result.status === "succeeded") {
          if (result.buildID !== build.buildID || result.buildVersion !== build.version) {
            throw new Error(`App ${app.id} Build replay version mismatch`);
          }
          completed = true;
          break;
        }
        await new Promise((resolve) => setTimeout(resolve, 5_000));
      }
      if (!completed) throw new Error(`App ${app.id} Build replay timed out`);
      if (build.buildID === app.activeBuildID) targetInstalled = true;
      // Fire Fuse prunes old, inactive Builds before admitting another one.
      // Keep the Console-selected Build protected throughout replay, not only
      // after the last materialization.
      if (app.activeBuildID === null || targetInstalled) {
        await runtime.selectBuild(app.ownerUserID, app.id, app.activeBuildID);
        selected = true;
      }
    }
    if (!selected) await runtime.selectBuild(app.ownerUserID, app.id, app.activeBuildID);
    if (app.configRevision > 0) {
      const projection = await config.projection(app.id);
      if (projection.revision !== app.configRevision) throw new Error(`App ${app.id} configuration changed during replay`);
      await compute.replaceConfiguration({ ownerUserID: app.ownerUserID, appID: app.id, ...projection });
    }
    for (const workspace of app.workspaces) {
      await compute.registerWorkspace({
        ownerUserID: app.ownerUserID, appID: app.id,
        workspaceID: workspace.id, workspaceSlug: workspace.slug,
      });
    }
    if (await runtime.activeBuild(app.ownerUserID, app.id) !== app.activeBuildID) {
      throw new Error(`App ${app.id} active Build readback mismatch`);
    }
    // Keep new Sessions closed until the control worker reconciles the edge
    // script and host Build together. Replay cannot prove the edge pointer.
  }
}
