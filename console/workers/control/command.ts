import { workspaceDatabases } from "../../core/workspace-databases/configured";
import { withWorkspaceDatabases } from "../../core/workspace-databases/compute-provider";
import { computeTenantResolver } from "../../core/db/compute-tenant";
import { runWorkerLoops } from "./loops";
import { DeploymentRecoveryWorker } from "../../core/deployment-recovery/worker";
import { RecoveryPrerequisiteRepository } from "../../core/db/recovery-prerequisite-repository";
import { controlDatabase, closeControlDatabase } from "../../core/db/database";
import { OperationRepository } from "../../core/db/operation-repository";
import { ReleaseRepository } from "../../core/db/release-repository";
import { configuredCloudflareAPIProvider } from "../../core/api-deployments/cloudflare-provider";
import { configuredAPIArtifactStore } from "../../core/api-deployments/s3-artifact-store";
import { APIDeploymentWorker } from "../../core/api-deployments/worker";
import { configuredDeploymentArtifactStore } from "../../core/deployment-uploads/s3-artifact-store";
import { configuredFireFuseSessionRuntimeProvider } from "../../core/session-runtime-materializations/fire-fuse-provider";
import { SessionRuntimeMaterializationWorker } from "../../core/session-runtime-materializations/worker";
import { LocalImageBuilder } from "../../core/session-runtime-materializations/local-builder";
import { configuredPreparedArtifactStore } from "../../core/session-runtime-materializations/prepared-artifact-store";
import { configuredFireFuseComputeRegistrationProvider } from "../../core/compute-registrations/fire-fuse-provider";
import { ComputeRegistrationWorker } from "../../core/compute-registrations/worker";
import { ComputeWorkspaceRegistrationWorker } from "../../core/compute-registrations/workspace-worker";
import { ComputeWorkspaceArchiveWorker } from "../../core/compute-registrations/workspace-archive-worker";
import { ComputeWorkspacePurgeWorker } from "../../core/compute-registrations/workspace-purge-worker";
import { ComputeAppArchiveWorker } from "../../core/compute-registrations/archive-worker";
import { ComputeRegistrationRepository } from "../../core/db/compute-registration-repository";
import { ConfigCipher } from "../../core/configuration/cipher";
import { ConfigRepository } from "../../core/configuration/repository";
import { configuredCloudflareConfigurationProvider } from "../../core/configuration/cloudflare-provider";
import { ConfigurationProjectionWorker } from "../../core/configuration/worker";
import { configuredCloudflareAppRoutingProvider } from "../../core/app-routing/cloudflare-provider";
import { ProductRepository } from "../../core/db/product-repository";
import { planFreshHostReplay, applyFreshHostReplay } from "../../core/host-replay/replay";
import { ReleaseActivationWorker } from "../../core/release-activation/worker";
import { configuredEdgeGateProvider } from "../../core/release-activation/edge-gate-provider";
import { ReleaseReconciler } from "../../core/release-activation/reconcile";
import { FleetPolicyWorker } from "../../core/fleets/policy-worker";
import { computePoolFetch } from "../../core/compute-registrations/pool-fetch";
import { FleetInstallationWorker } from "../../core/fleets/worker";
import { FleetPackageWorker } from "../../core/fleets/package-worker";
import { FleetAttachmentWorker } from "../../core/fleets/attachment-worker";
import { FleetConfigurationProvider } from "../../core/fleets/configuration-provider";

const workerID = process.env.CANTELOP_OPERATION_WORKER_ID?.trim();
if (!workerID) throw new Error("CANTELOP_OPERATION_WORKER_ID is required");
const arguments_ = process.argv.slice(2);
const once = arguments_.includes("--once");
const check = arguments_.includes("--check");
const reconcileWorkspacePurges = arguments_.includes("--reconcile-workspace-purges");
const replayHost = arguments_.includes("--replay-host");
const apply = arguments_.includes("--apply");
if (arguments_.some((argument) => !["--once", "--check", "--reconcile-workspace-purges", "--replay-host", "--apply"].includes(argument)) ||
    [once, check, reconcileWorkspacePurges, replayHost].filter(Boolean).length > 1 || (apply && !reconcileWorkspacePurges && !replayHost)) {
  throw new Error("usage: cantelop-control-worker [--once | --check | --reconcile-workspace-purges [--apply] | --replay-host [--apply]]");
}

const pool = controlDatabase();
const computeRequest = computePoolFetch(pool, process.env);
const tenantForApp = computeTenantResolver(pool);
const databases = workspaceDatabases();
const computeProvider = withWorkspaceDatabases(configuredFireFuseComputeRegistrationProvider(process.env, computeRequest, tenantForApp), databases);
const appRoutingProvider = configuredCloudflareAppRoutingProvider();
const configuration = new ConfigRepository(pool, ConfigCipher.configured());
const apiConfiguration = new FleetConfigurationProvider(configuredCloudflareConfigurationProvider(), configuredEdgeGateProvider());
const artifactStore = configuredAPIArtifactStore();
const worker = new APIDeploymentWorker(
  workerID,
  new OperationRepository(pool),
  new ReleaseRepository(pool),
  artifactStore,
  configuredCloudflareAPIProvider(),
  configuration,
  apiConfiguration,
);
const sessionRuntimeWorker = new SessionRuntimeMaterializationWorker(
  workerID,
  new OperationRepository(pool),
  new ReleaseRepository(pool),
  configuredDeploymentArtifactStore(),
  new LocalImageBuilder(),
  configuredPreparedArtifactStore(),
  configuredFireFuseSessionRuntimeProvider(process.env, computeRequest, tenantForApp),
);
const recoveryWorker = new DeploymentRecoveryWorker(workerID, new OperationRepository(pool), new ReleaseRepository(pool),
  configuredFireFuseSessionRuntimeProvider(process.env, computeRequest, tenantForApp), new RecoveryPrerequisiteRepository(pool));
const registrationWorker = new ComputeRegistrationWorker(
  workerID,
  new OperationRepository(pool),
  computeProvider,
  appRoutingProvider,
);
const workspaceRegistrationWorker = new ComputeWorkspaceRegistrationWorker(
  workerID,
  new OperationRepository(pool),
  new ComputeRegistrationRepository(pool),
  computeProvider,
);
const workspaceArchiveWorker = new ComputeWorkspaceArchiveWorker(
  workerID,
  new OperationRepository(pool),
  computeProvider,
);
const workspacePurgeWorker = new ComputeWorkspacePurgeWorker(
  workerID,
  new OperationRepository(pool),
  computeProvider,
  ProductRepository.fromPool(pool),
);
const archiveWorker = new ComputeAppArchiveWorker(
  workerID,
  new OperationRepository(pool),
  computeProvider,
  appRoutingProvider,
  ProductRepository.fromPool(pool),
  new ReleaseRepository(pool),
  configuredEdgeGateProvider(),
  configuredFireFuseSessionRuntimeProvider(process.env, computeRequest, tenantForApp),
  configuredCloudflareAPIProvider(),
);
const configurationWorker = new ConfigurationProjectionWorker(
  workerID,
  new OperationRepository(pool),
  configuration,
  apiConfiguration,
  computeProvider,
  new ReleaseRepository(pool),
);
const activationWorker = new ReleaseActivationWorker(
  workerID,
  new OperationRepository(pool),
  new ReleaseRepository(pool),
  configuredEdgeGateProvider(),
  configuredFireFuseSessionRuntimeProvider(process.env, computeRequest, tenantForApp),
  configuration,
  apiConfiguration,
  undefined,
  (appID, releaseID, closeAdmission) => workspaceDatabases().migrateRelease(appID, releaseID, closeAdmission),
);

try {
  if (check) {
    await Promise.all([pool.query("SELECT 1"), artifactStore.check()]);
  } else if (reconcileWorkspacePurges) {
    const products = ProductRepository.fromPool(pool);
    const before = await products.workspacePurgeReconciliationStatus();
    if (!apply) {
      console.log(JSON.stringify({ mode: "dry_run", ...before }));
    } else {
      let queued = 0;
      for (;;) {
        const batch = await products.queueArchivedWorkspacePurges();
        queued += batch;
        if (batch === 0) break;
      }
      const after = await products.workspacePurgeReconciliationStatus();
      console.log(JSON.stringify({ mode: "apply", queued, before, after }));
    }
  } else if (replayHost) {
    const plan = await planFreshHostReplay(pool);
    const summary = { apps: plan.length, workspaces: plan.reduce((count, app) => count + app.workspaces.length, 0),
      builds: plan.reduce((count, app) => count + app.builds.length, 0) };
    if (apply) {
      await applyFreshHostReplay(plan, computeProvider,
        configuredFireFuseSessionRuntimeProvider(process.env, computeRequest, tenantForApp), configuration,
        configuredPreparedArtifactStore());
      console.log(JSON.stringify({ mode: "applied", ...summary }));
    } else {
      console.log(JSON.stringify({ mode: "dry_run", ...summary }));
    }
  } else {
    await worker.recoverStale();
    // The audit makes slow provider calls per App; run it beside the worker
    // loops so an unreachable provider cannot stall all other operations.
    const releaseAudit = new ReleaseReconciler(
      new ReleaseRepository(pool), configuredEdgeGateProvider(),
      configuredFireFuseSessionRuntimeProvider(process.env, computeRequest, tenantForApp),
    ).runOnce().then((audit) => {
      if (audit.unreadable) console.error(`release gate audit could not read ${audit.unreadable} Apps`);
      if (audit.unsupported) console.error(`release gate audit skipped ${audit.unsupported} Apps whose live release predates SDK 1.0`);
    }, (error: unknown) => { console.error("release gate audit failed", error); });
    const fleetPolicies = new FleetPolicyWorker(pool,
      process.env.CANTELOP_COMPUTE_INGRESS_URL!, process.env.CANTELOP_COMPUTE_CONTROL_TOKEN!, computeRequest);
    const fleetInstallations = new FleetInstallationWorker(pool, configuration, fleetPolicies);
    const fleetPackages = new FleetPackageWorker(pool, artifactStore, configuredEdgeGateProvider());
    const fleetAttachments = new FleetAttachmentWorker(workerID, pool, new OperationRepository(pool), new ReleaseRepository(pool),
      configuredPreparedArtifactStore(), configuredFireFuseSessionRuntimeProvider(process.env, computeRequest, tenantForApp));
    await runWorkerLoops([registrationWorker, workspaceRegistrationWorker, workspaceArchiveWorker, workspacePurgeWorker, archiveWorker,
      configurationWorker, worker, sessionRuntimeWorker, recoveryWorker, activationWorker, fleetPackages, fleetAttachments, fleetInstallations, fleetPolicies], once);
    await releaseAudit;
  }
} finally {
  await closeControlDatabase();
}
