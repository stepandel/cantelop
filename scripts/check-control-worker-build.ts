import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { buildControlWorker } from "./build-control-worker";

const directory = await mkdtemp(join(tmpdir(), "cantelop-control-worker-check-"));
try {
  const output = join(directory, "probe");
  await buildControlWorker(output, resolve(import.meta.dir, "../console/scripts/control-worker-build-probe.ts"));
  const child = Bun.spawn(["docker", "run", "--rm", "--platform", "linux/amd64", "-v", `${output}:/probe:ro`, "debian:bookworm-slim", "/probe"], { stdout: "inherit", stderr: "inherit" });
  if (await child.exited !== 0) throw new Error("compiled Linux control worker qualification failed");
} finally { await rm(directory, { recursive: true, force: true }); }
