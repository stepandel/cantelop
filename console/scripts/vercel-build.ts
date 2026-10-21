import { spawn } from "node:child_process";
import { sourceInfo } from "../web/lib/source-info";
import { verifyPublicSource } from "./verify-public-source";
import { configuredAppDomain, configuredConsoleOrigins } from "../core/deployment";

type BuildEnvironment = Record<string, string | undefined>;

export function vercelBuildScripts(environment: BuildEnvironment): string[] {
  const target = environment.VERCEL_ENV?.trim();
  const hasControlDatabase = Boolean(environment.CANTELOP_DATABASE_URL?.trim());
  const hasObservabilityOverride = Boolean(
    environment.CANTELOP_OBSERVABILITY_DATABASE_URL?.trim(),
  );
  const ingestionToken = environment.CANTELOP_OBSERVABILITY_INGEST_TOKEN?.trim();
  const edgeIngestionToken = environment.CANTELOP_EDGE_OBSERVABILITY_INGEST_TOKEN?.trim();
  const computeToken = environment.CANTELOP_COMPUTE_CONTROL_TOKEN?.trim();
  const gatewayProductToken = environment.CANTELOP_GATEWAY_PRODUCT_CONTROL_TOKEN?.trim();

  if (target === "production" && !hasControlDatabase) {
    throw new Error("production requires CANTELOP_DATABASE_URL");
  }
  if (target === "production" && !/^[0-9a-f]{64}$/.test(ingestionToken ?? "")) {
    throw new Error("production requires a valid CANTELOP_OBSERVABILITY_INGEST_TOKEN");
  }
  if (target === "production" && (!/^[0-9a-f]{64}$/.test(edgeIngestionToken ?? "") ||
      edgeIngestionToken === ingestionToken)) {
    throw new Error("production requires a distinct valid CANTELOP_EDGE_OBSERVABILITY_INGEST_TOKEN");
  }
  if (target === "production" && (!/^[0-9a-f]{64}$/.test(gatewayProductToken ?? "") ||
      gatewayProductToken === ingestionToken || gatewayProductToken === edgeIngestionToken ||
      gatewayProductToken === computeToken)) {
    throw new Error("production requires a distinct valid CANTELOP_GATEWAY_PRODUCT_CONTROL_TOKEN");
  }
  if (target === "production") {
    for (const name of ["CANTELOP_TURSO_ORGANIZATION", "CANTELOP_TURSO_GROUP"]) {
      if (!/^[a-z0-9][a-z0-9-]{0,62}$/.test(environment[name] ?? "")) throw new Error(`production requires ${name}`);
    }
    if (!/^[a-z0-9]{1,16}$/.test(environment.CANTELOP_TURSO_ENVIRONMENT ?? "")) throw new Error("production requires CANTELOP_TURSO_ENVIRONMENT");
    if (!environment.CANTELOP_TURSO_PLATFORM_TOKEN?.trim()) throw new Error("production requires CANTELOP_TURSO_PLATFORM_TOKEN");
    if (!/^[A-Za-z0-9_-]{43}$/.test(environment.CANTELOP_CONFIG_ENCRYPTION_KEY ?? "")) throw new Error("production requires CANTELOP_CONFIG_ENCRYPTION_KEY");
    const capabilityKey = environment.CANTELOP_WORKSPACE_DATABASE_CAPABILITY_KEY;
    if (!/^[0-9a-f]{64}$/.test(capabilityKey ?? "") || [computeToken, gatewayProductToken, ingestionToken, edgeIngestionToken].includes(capabilityKey)) throw new Error("production requires a distinct CANTELOP_WORKSPACE_DATABASE_CAPABILITY_KEY");
    if (!sourceInfo(environment)) throw new Error("production requires an exact public source revision via CANTELOP_SOURCE_COMMIT or VERCEL_GIT_COMMIT_SHA");
    configuredAppDomain(environment);
    configuredConsoleOrigins(environment);
  }
  if (!hasControlDatabase && hasObservabilityOverride) {
    throw new Error(
      "CANTELOP_OBSERVABILITY_DATABASE_URL cannot be used without CANTELOP_DATABASE_URL",
    );
  }

  return [
    ...(hasControlDatabase ? ["db:migrate", "observability:db:migrate"] : []),
    "build",
  ];
}

async function runPackageScript(script: string): Promise<void> {
  await new Promise<void>((resolve, reject) => {
    const child = spawn("bun", ["run", script], { stdio: "inherit" });
    child.once("error", reject);
    child.once("exit", (code, signal) => {
      if (code === 0) resolve();
      else reject(new Error(`bun run ${script} failed with ${signal ?? `exit code ${code}`}`));
    });
  });
}

if (import.meta.main) {
  verifyPublicSource();
  for (const script of vercelBuildScripts(process.env)) await runPackageScript(script);
}
