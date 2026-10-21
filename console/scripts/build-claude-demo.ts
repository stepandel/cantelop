import { cp, mkdir, rm } from "node:fs/promises";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { buildAppArtifacts } from "@cantelop/sdk/build";

const root = fileURLToPath(new URL("../", import.meta.url));
const build = await buildAppArtifacts({
  definition: join(root, "examples/claude-demo/cantelop.ts"),
  outdir: join(root, ".cantelop/claude-demo"),
  projectRoot: root,
});
const generated = join(root, "web/app/api/examples/claude-plan/generated");
await rm(generated, { recursive: true, force: true });
await mkdir(generated, { recursive: true });
await cp(build.backend.directory, generated, { recursive: true });
