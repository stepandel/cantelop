import { expect, test } from "bun:test";
import { createHash } from "node:crypto";
import { mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { verifyPublicSource } from "./verify-public-source";

const digest = (value) => createHash("sha256").update(value).digest("hex");

test.each(["public", "private"])("%s deployment verification works without Git and rejects changed or newly added source", (visibility) => {
  const root = mkdtempSync(join(tmpdir(), "cantelop-source-test-"));
  try {
    mkdirSync(join(root, "console"));
    const policy = readFileSync(new URL("../../.public-source-policy.json", import.meta.url), "utf8");
    const pin = { schema_version: 1, repository: "https://github.com/stepandel/cantelop", commit: null, snapshot_sha256: null };
    const sources = {
      ".public-source-policy.json": policy,
      "console/public-source.json": JSON.stringify(pin, null, 2) + "\n",
      "main.ts": "export const version = 1;\n",
      "console/vercel.json": "{\"buildCommand\":\"bun run vercel-build\"}\n",
    };
    for (const [name, data] of Object.entries(sources)) writeFileSync(join(root, name), data, { mode: 0o644 });
    const manifest = JSON.stringify({ schema_version: 1, files: Object.fromEntries(Object.entries(sources).map(([name, data]) => [name, { sha256: digest(data), mode: 0o644 }])) }, null, 2) + "\n";
    writeFileSync(join(root, "SOURCE_SNAPSHOT.json"), manifest);
    pin.visibility = visibility;
    pin.commit = "a".repeat(40);
    pin.snapshot_sha256 = digest(manifest);
    writeFileSync(join(root, "console/public-source.json"), JSON.stringify(pin));
    expect(() => verifyPublicSource(root)).not.toThrow();
    writeFileSync(join(root, "vercel.json"), "{}");
    expect(() => verifyPublicSource(root)).toThrow("file list changed");
    expect(() => verifyPublicSource(root, true)).not.toThrow();
    writeFileSync(join(root, "console/vercel.json"), "{}");
    expect(() => verifyPublicSource(root, true)).toThrow("source changed");
    writeFileSync(join(root, "console/vercel.json"), sources["console/vercel.json"]);
    rmSync(join(root, "vercel.json"));
    writeFileSync(join(root, "main.ts"), "export const version = 2;\n");
    expect(() => verifyPublicSource(root)).toThrow("source changed");
    writeFileSync(join(root, "main.ts"), sources["main.ts"]);
    writeFileSync(join(root, "new-route.ts"), "export const route = 1;\n");
    expect(() => verifyPublicSource(root)).toThrow("file list changed");
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});
