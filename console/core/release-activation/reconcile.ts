import type { ReleaseRepository, ProductRelease } from "../db/release-repository";
import type { SessionRuntimeProvider } from "../session-runtime-materializations/provider";
import type { EdgeGateProvider } from "./edge-gate-provider";

const buildPattern = /^sha256:[0-9a-f]{64}$/;

// A read-only startup audit schedules repair only for mismatched Apps. A
// successful activation operation is not proof that a recreated provider
// still has its former gate state or selected Build.
export class ReleaseReconciler {
  constructor(
    private readonly releases: Pick<ReleaseRepository,
      "activeReleaseCandidates" | "getForUser" | "queueActiveReconciliation">,
    private readonly edge: Pick<EdgeGateProvider, "read">,
    private readonly runtime: Pick<SessionRuntimeProvider, "releaseAdmission" | "activeBuild">,
  ) {}

  async runOnce(): Promise<{ checked: number; queued: number; unreadable: number; unsupported: number }> {
    let checked = 0;
    let queued = 0;
    let unreadable = 0;
    let unsupported = 0;
    for (const candidate of await this.releases.activeReleaseCandidates()) {
      const release = await this.releases.getForUser(candidate.ownerUserID, candidate.appID, candidate.releaseID);
      // A live release from before SDK 1.0 cannot be reactivated, so queueing
      // its reconciliation would only fail; it is reported instead.
      if (!release.definition) {
        unsupported++;
        continue;
      }
      let mismatch: boolean;
      try {
        mismatch = await this.mismatched(candidate.ownerUserID, candidate.appID, release);
      } catch {
        unreadable++;
        continue;
      }
      checked++;
      if (mismatch && await this.releases.queueActiveReconciliation(candidate.ownerUserID, candidate.appID, candidate.releaseID)) {
        queued++;
      }
    }
    return { checked, queued, unreadable, unsupported };
  }

  private async mismatched(owner: string, appID: string, release: ProductRelease): Promise<boolean> {
    const script = release.components.api.output?.script_name;
    const build = release.components.session_runtime.output?.build_id ??
      release.components.session_runtime.output?.provider_build_id;
    if ((script !== release.id && !(typeof script === "string" && /^pkg_[0-9a-f]{32}$/.test(script) &&
        release.components.api.output?.package_id === script)) || typeof build !== "string" || !buildPattern.test(build)) return true;
    const [edge, host, selectedBuild] = await Promise.all([
      this.edge.read(appID), this.runtime.releaseAdmission(owner, appID), this.runtime.activeBuild(owner, appID),
    ]);
    return edge.closed || host.closed || edge.generation !== host.generation ||
      edge.script_name !== script || selectedBuild !== build;
  }
}
