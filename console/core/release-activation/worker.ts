import { AppEnvironmentError, validateAppEnvironment } from "../configuration/declarations";
import { DatabaseSchemaError } from "../workspace-databases/schema";

import type { OperationRepository, ControlOperation } from "../db/operation-repository";
import { releaseBuildID } from "../db/release-build-id";
import type { ReleaseRepository, ProductRelease } from "../db/release-repository";
import type { ConfigRepository } from "../configuration/repository";
import type { APIConfigurationProvider } from "../configuration/provider";
import { SessionRuntimeProviderError, type SessionRuntimeProvider } from "../session-runtime-materializations/provider";
import type { EdgeGateProvider, EdgeGateState } from "./edge-gate-provider";

const generationPattern = /^act_[0-9a-f]{32}$/;
const retryMilliseconds = 30_000;

// A deterministic failure that retrying the same generation cannot repair.
// The operation fails so deployment, rollback, and recovery are unblocked.
class TerminalActivationError extends Error {
  constructor(readonly code: string) { super(code); }
}

interface ActivationInput {
  targetReleaseID: string;
  previousReleaseID: string | null;
  generation: string;
}

export class ReleaseActivationWorker {
  constructor(
    private readonly workerID: string,
    private readonly operations: Pick<OperationRepository, "claim" | "getForUser" | "retry" | "succeed" | "fail">,
    private readonly releases: Pick<ReleaseRepository,
      "withDeploymentLock" | "getForUser" | "commitActivation" | "deploymentManifest">,
    private readonly edge: Pick<EdgeGateProvider, "read" | "close" | "select" | "open">,
    private readonly runtime: Pick<SessionRuntimeProvider, "releaseAdmission" | "setReleaseAdmission" | "selectBuild" | "activeBuild" | "retainsBuild">,
    private readonly configuration: Pick<ConfigRepository, "projection">,
    private readonly apiConfiguration: APIConfigurationProvider,
    private readonly now: () => Date = () => new Date(),
    private readonly migrateDatabase?: (appID: string, releaseID: string, closeAdmission: () => Promise<void>) => Promise<void>,
  ) {
    if (!/^[A-Za-z0-9][A-Za-z0-9_.:-]{0,63}$/.test(workerID)) throw new TypeError("invalid activation worker ID");
  }

  async runOnce(): Promise<boolean> {
    const operation = await this.operations.claim(this.workerID, "activate_release");
    if (!operation) return false;
    await this.releases.withDeploymentLock(operation.appID, async () => {
      const current = await this.operations.getForUser(operation.ownerUserID, operation.id);
      if (current.status !== "running" || current.lockedBy !== this.workerID) return;
      try {
        await this.activate(operation);
      } catch (error) {
        // An uncertain provider response cannot be treated as a successful
        // transition. Retrying the same generation converges from every
        // partial closure, selection, commit, and reopening boundary.
        const latest = await this.operations.getForUser(operation.ownerUserID, operation.id);
        if (latest.status === "running" && latest.lockedBy === this.workerID) {
          if (error instanceof AppEnvironmentError) {
            await this.operations.fail(operation.id, this.workerID, "app_environment_missing_or_invalid");
          } else if (error instanceof DatabaseSchemaError) {
            await this.operations.fail(operation.id, this.workerID, `database_${error.code}`);
          } else if (error instanceof TerminalActivationError) {
            await this.operations.fail(operation.id, this.workerID, error.code);
          } else {
            await this.operations.retry(operation.id, this.workerID, new Date(this.now().getTime() + retryMilliseconds));
          }
        }
      }
    });
    return true;
  }

  private async activate(operation: ControlOperation): Promise<void> {
    const input = activationInput(operation);
    const target = await this.releases.getForUser(operation.ownerUserID, operation.appID, input.targetReleaseID);
    const scriptName = targetScript(target);
    const buildID = targetBuildID(target);
    const appID = operation.appID;
    const owner = operation.ownerUserID;
    const generation = input.generation;

    // A newer generation already owns the gates; this one can never close them.
    const existing = await this.edge.read(appID);
    if (existing.generation !== null && existing.generation > generation) {
      throw new TerminalActivationError("activation_superseded");
    }

    // Fail before closing admission or migrating when the host has dropped the
    // target Build, so the live release keeps serving. An unknown answer falls
    // through to Build selection, which reports a missing Build itself.
    if (await this.runtime.retainsBuild?.(owner, appID, buildID).catch(() => true) === false) {
      throw new TerminalActivationError("target_build_missing");
    }

    const projection = await this.configuration.projection(appID);
    const manifest = await this.releases.deploymentManifest(appID, target.id);
    if (manifest === null) throw new TerminalActivationError("release_unsupported");
    validateAppEnvironment(manifest, projection.values);
    if (!projection.systemBindings?.length) throw new AppEnvironmentError();
    const closeAdmission = async () => {
      await this.edge.close(appID, generation);
      await this.runtime.setReleaseAdmission(owner, appID, generation, true);
      assertEdgeState(await this.edge.read(appID), generation, true);
      assertHostState(await this.runtime.releaseAdmission(owner, appID), generation, true);
    };
    // Additive migrations preserve availability on failure. Explicit destructive
    // changes close admission first and remain closed if their application fails.
    await this.migrateDatabase?.(appID, target.id, closeAdmission);
    await closeAdmission();

    // Rollback targets may carry old secrets. Reproject the current values
    // while the gate is closed and configuration writes hold the same lock.
    if (projection.revision > 0 || projection.systemBindings?.length) {
      await this.apiConfiguration.replaceConfiguration({ appID, scriptName, ...projection });
    }
    try {
      await this.runtime.selectBuild(owner, appID, buildID);
    } catch (error) {
      // Retention pruned the target Build; no retry can restore it.
      if (error instanceof SessionRuntimeProviderError && error.progress === "provider_http_404") {
        throw new TerminalActivationError("target_build_missing");
      }
      throw error;
    }
    await this.edge.select(appID, generation, scriptName);
    if (await this.runtime.activeBuild(owner, appID) !== buildID) throw new Error("selected Build readback mismatch");
    assertEdgeState(await this.edge.read(appID), generation, true, scriptName);

    await this.releases.commitActivation(owner, appID, target.id, input.previousReleaseID);
    await this.runtime.setReleaseAdmission(owner, appID, generation, false);
    await this.edge.open(appID, generation);
    assertHostState(await this.runtime.releaseAdmission(owner, appID), generation, false);
    if (await this.runtime.activeBuild(owner, appID) !== buildID) throw new Error("open Build readback mismatch");
    assertEdgeState(await this.edge.read(appID), generation, false, scriptName);
    await this.operations.succeed(operation.id, this.workerID, { target_release_id: target.id, generation });
  }
}

// Every SDK 1.0 release deploys its generated Edge Worker under its own release
// ID. A release without one predates SDK 1.0 and can no longer be activated.
function targetScript(release: ProductRelease): string {
  const script = release.components.api.output?.script_name;
  if (script !== release.id) throw new TerminalActivationError("release_unsupported");
  return script;
}

function activationInput(operation: ControlOperation): ActivationInput {
  const input = operation.input;
  if (Object.keys(input).sort().join(",") !== "generation,previous_release_id,target_release_id" ||
      typeof input.target_release_id !== "string" || !/^rel_[0-9a-f]{32}$/.test(input.target_release_id) ||
      (input.previous_release_id !== null && (typeof input.previous_release_id !== "string" ||
        !/^rel_[0-9a-f]{32}$/.test(input.previous_release_id))) ||
      typeof input.generation !== "string" || !generationPattern.test(input.generation)) {
    throw new TypeError("invalid release activation operation");
  }
  return { targetReleaseID: input.target_release_id,
    previousReleaseID: input.previous_release_id, generation: input.generation };
}

function targetBuildID(release: ProductRelease): string {
  if (release.components.session_runtime.status !== "succeeded") throw new Error("Session runtime is not staged");
  const build = releaseBuildID(release);
  if (build === null) throw new Error("release Build identity is invalid");
  return build;
}

function assertEdgeState(state: EdgeGateState, generation: string, closed: boolean, scriptName?: string): void {
  if (state.generation !== generation || state.closed !== closed ||
      (scriptName !== undefined && state.script_name !== scriptName)) throw new Error("edge release gate readback mismatch");
}

function assertHostState(state: { generation: string; closed: boolean }, generation: string, closed: boolean): void {
  if (state.generation !== generation || state.closed !== closed) throw new Error("host release gate readback mismatch");
}
