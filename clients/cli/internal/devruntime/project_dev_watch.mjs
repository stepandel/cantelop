const sdk = await loadSDK("watchLocalProject");
if (sdk.CANTELOP_LOCAL_DATABASE_PROTOCOL_VERSION !== 1)
  throw new Error(
    "Local Workspace databases require a newer @cantelop/sdk; run npm install @cantelop/sdk@latest",
  );
const report = (event) =>
  process.stdout.write(
    "CANTELOP_LOCAL_BUILD_EVENT " + JSON.stringify(event) + "\n",
  );
let watcher;
if (sdk.CANTELOP_CLI_BUILD_PROTOCOL_VERSION === 6) {
  const { createRequire } = await import("node:module");
  const { mkdir, cp, rename, rm, readFile, readdir, stat } = await import("node:fs/promises");
  const { join } = await import("node:path");
  const require = createRequire(import.meta.resolve("@cantelop/sdk/build"));
  const esbuild = require("esbuild");
  const stage = process.argv[2] + "-stage";
  const schemaDirectory = join(process.argv[6], "db");
  const schemaPath = join(schemaDirectory, "schema.ts");
  let artifacts, buildError;
  let watchFiles = [process.argv[1]];
  let first = true;
  let resolveInitial, rejectInitial;
  const initialBuild = new Promise((resolve, reject) => {
    resolveInitial = resolve;
    rejectInitial = reject;
  });
  const buildContext = await esbuild.context({
    stdin: {
      contents: 'import "cantelop-local-definition"; import "cantelop-local-schema";',
      resolveDir: process.argv[6],
      loader: "ts",
    },
    bundle: true,
    write: false,
    platform: "node",
    format: "esm",
    packages: "external",
    logLevel: "silent",
    plugins: [{
      name: "cantelop-local-artifacts",
      setup(build) {
        // One compilation produces matching Edge, runtime and backend artifacts.
        build.onStart(async () => {
          buildError = undefined;
          try {
            await rm(stage, { recursive: true, force: true });
            artifacts = await sdk.buildAppArtifacts({
              definition: process.argv[1],
              outdir: stage,
              projectRoot: process.argv[6],
              runtimeOrigin: process.argv[5],
            });
            if (!Array.isArray(artifacts.watchFiles))
              throw new Error("App watching requires a newer @cantelop/sdk; run npm install @cantelop/sdk@latest");
            watchFiles = artifacts.watchFiles;
          } catch (failure) {
            buildError = failure;
            watchFiles = [...new Set([...watchFiles, ...(failure.watchFiles ?? [])])];
          }
        });
        build.onResolve({ filter: /^cantelop-local-(definition|schema)$/ }, args =>
          ({ path: args.path, namespace: "cantelop-local" }));
        build.onLoad({ filter: /definition$/, namespace: "cantelop-local" }, () => ({
          contents: "",
          watchFiles,
          ...(buildError ? { errors: [{ text: String(buildError.message ?? buildError) }] } : {}),
        }));
        // Schemas are discovered independently of App imports. Watch their
        // dependency graph and the missing path so adding a schema also rebuilds.
        build.onLoad({ filter: /schema$/, namespace: "cantelop-local" }, async () => {
          let contents = "";
          try { contents = await readFile(schemaPath, "utf8"); }
          catch (failure) { if (failure.code !== "ENOENT") throw failure; }
          let watchDirectory = schemaDirectory;
          try { await stat(schemaDirectory); }
          catch (failure) { if (failure.code !== "ENOENT") throw failure; watchDirectory = process.argv[6]; }
          return { contents, loader: "ts", resolveDir: schemaDirectory,
            watchFiles: [schemaPath], watchDirs: [watchDirectory] };
        });
        build.onEnd(async (result) => {
          let runtimeChanged = false;
          let error = result.errors.map(error => error.text).join("\n");
          if (!error) try {
            const app = artifacts.apps.find(app => app.name === process.argv[3]);
            if (!app) throw new Error("Selected App is not defined");
            let previousRuntime;
            try { previousRuntime = JSON.parse(await readFile(join(process.argv[4], "cantelop-runtime.json"), "utf8")); }
            catch (failure) { if (failure.code !== "ENOENT") throw failure; }
            runtimeChanged = JSON.stringify(previousRuntime) !== JSON.stringify(app.runtime.manifest);
            for (const [source, target] of [
              [app.runtime.directory, process.argv[4]],
              [app.edge.directory, process.argv[2]],
              [artifacts.backend.directory, join(process.argv[6], ".cantelop/client")],
            ]) {
              await mkdir(target, { recursive: true });
              for (const name of await readdir(source)) {
                await cp(join(source, name), join(target, name + ".next"));
                await rename(join(target, name + ".next"), join(target, name));
              }
            }
          } catch (failure) { error = String(failure.message ?? failure); }
          if (first) {
            first = false;
            if (error) rejectInitial(new Error(error));
            else resolveInitial();
          } else {
            report({ component: "api", ...(error ? { error } : {}) });
            if (runtimeChanged || error)
              report({ component: "session-runtime", ...(error ? { error } : {}) });
          }
        });
      },
    }],
  });
  try {
    await mkdir(join(process.argv[6], ".cantelop/client"), { recursive: true });
    await buildContext.watch();
    await initialBuild;
  } catch (failure) {
    await buildContext.dispose();
    throw failure;
  }
  watcher = { dispose: () => buildContext.dispose() };
} else
  watcher = await sdk.watchLocalProject({
    apiEntrypoint: process.argv[1],
    apiOutdir: process.argv[2],
    sessionEntrypoint: process.argv[3],
    sessionRuntimeOutdir: process.argv[4],
    runtimeOrigin: process.argv[5],
    onBuild: report,
  });
process.stdout.write("CANTELOP_LOCAL_BUILD_READY\n");
let closing = false;
const shutdown = async () => {
  if (closing) return;
  closing = true;
  await watcher.dispose();
  process.exit(0);
};
process.once("SIGINT", shutdown);
process.once("SIGTERM", shutdown);
await new Promise(() => {});
