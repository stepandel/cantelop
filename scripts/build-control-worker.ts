import { mkdtemp, rm } from "node:fs/promises";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

const require = createRequire(resolve(import.meta.dir, "../console/package.json"));
const { build: bundle } = require("esbuild");
// The worker uses SQLite snapshots and DDL, not Drizzle Kit's other adapters.
const unsupportedDrivers = new Set([
  "@electric-sql/pglite", "postgres", "@vercel/postgres",
  "@neondatabase/serverless", "mysql2", "mysql2/promise",
  "@aws-sdk/client-rds-data", "@planetscale/database", "better-sqlite3",
]);

export async function buildControlWorker(
  output: string,
  entrypoint = resolve(import.meta.dir, "../console/workers/control/command.ts"),
): Promise<void> {
  const temporary = await mkdtemp(join(tmpdir(), "cantelop-control-worker-build-"));
  try {
    const module = join(temporary, "control-worker.mjs");
    // Preserve CommonJS initialization for Drizzle's circular exports. Bundling
    // the complete module first also avoids Bun 1.3.10 misbinding AWS symbols.
    await bundle({
      entryPoints: [entrypoint], outfile: module, bundle: true,
      platform: "node", format: "esm", treeShaking: false,
      plugins: [{ name: "sqlite-schema-only", setup(build: any) {
        build.onResolve({ filter: /^drizzle-(orm|kit)(\/.*)?$/ }, (args: any) => ({ path: require.resolve(args.path) }));
        build.onResolve({ filter: /.*/ }, (args: any) => unsupportedDrivers.has(args.path)
          ? { path: args.path, namespace: "unsupported-database-driver" } : undefined);
        build.onLoad({ filter: /.*/, namespace: "unsupported-database-driver" }, (args: any) => ({
          loader: "js",
          contents: `const fail = function () { throw new Error(${JSON.stringify(`Unsupported worker database driver: ${args.path}`)}); }; module.exports = new Proxy(fail, { get(_, key) { return key === "__esModule" ? false : fail; } });`,
        }));
      } }],
    });
    const result = await Bun.build({ entrypoints: [module], compile: { target: "bun-linux-x64", outfile: resolve(output) } });
    if (!result.success) throw new AggregateError(result.logs, "control worker compilation failed");
  } finally { await rm(temporary, { recursive: true, force: true }); }
}

if (import.meta.main) {
  const output = process.argv[2];
  if (!output) throw new Error("control worker output path is required");
  await buildControlWorker(output);
}
