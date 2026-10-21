#!/usr/bin/env node
// Refresh metadata only; bundled output still needs artifact-specific review.
import { createRequire } from "node:module";
import { createHash } from "node:crypto";
import { readFile, writeFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const require = createRequire(import.meta.url);
const ts = require(path.join(root, "console/node_modules/typescript"));
const lockfiles = ["console/bun.lock", "edge/workers/bun.lock", "examples/hello/package-lock.json", "tests/benchmarks/session-activation/package-lock.json"];
const hashes = {};
const packages = new Map();

function add(name, version, license, lockfile) {
  const key = `${name}@${version}`;
  const existing = packages.get(key) ?? { name, version, license, lockfiles: [] };
  if (license && existing.license && JSON.stringify(license) !== JSON.stringify(existing.license)) throw new Error(`conflicting license metadata: ${key}`);
  if (license) existing.license = license;
  if (!existing.lockfiles.includes(lockfile)) existing.lockfiles.push(lockfile);
  packages.set(key, existing);
}

// bun.lock keys nest dependencies as "parent/child"; each level is its own node_modules directory.
function installedPath(key) {
  const parts = key.split("/"), names = [];
  for (let index = 0; index < parts.length; index++) names.push(parts[index].startsWith("@") ? `${parts[index]}/${parts[++index]}` : parts[index]);
  return names.join("/node_modules/");
}

for (const lockfile of lockfiles) {
  const text = await readFile(path.join(root, lockfile), "utf8");
  hashes[lockfile] = createHash("sha256").update(text).digest("hex");
  const parsed = ts.parseConfigFileTextToJson(lockfile, text);
  if (parsed.error) throw new Error(`cannot parse ${lockfile}`);
  const data = parsed.config;
  if (lockfile.endsWith("bun.lock")) {
    for (const [key, item] of Object.entries(data.packages)) {
      if (item[0] === "@cantelop/edge-contracts@file:../contracts") continue;
      const separator = item[0].lastIndexOf("@");
      const name = item[0].slice(0, separator), version = item[0].slice(separator + 1);
      let license;
      try {
        const metadata = JSON.parse(await readFile(path.join(root, path.dirname(lockfile), "node_modules", installedPath(key), "package.json"), "utf8"));
        if (metadata.name === name && metadata.version === version) license = metadata.license;
      } catch (error) {
        if (error.code !== "ENOENT" && error.code !== "ENOTDIR") throw error;
      }
      add(name, version, license, lockfile);
    }
  } else {
    for (const [key, metadata] of Object.entries(data.packages)) {
      if (!key) continue;
      add(metadata.name ?? key.split("node_modules/").at(-1), metadata.version, metadata.license, lockfile);
    }
  }
}

const missing = [...packages.values()].filter(item => !item.license);
let cursor = 0;
await Promise.all(Array.from({ length: Math.min(6, missing.length) }, async () => {
  while (cursor < missing.length) {
    const item = missing[cursor++];
    const url = `https://registry.npmjs.org/${encodeURIComponent(item.name)}/${encodeURIComponent(item.version)}`;
    const response = await fetch(url, { signal: AbortSignal.timeout(30_000) });
    if (!response.ok) throw new Error(`registry metadata failed: ${item.name}@${item.version} (${response.status})`);
    const metadata = await response.json();
    if (!metadata.license) throw new Error(`missing upstream license metadata: ${item.name}@${item.version}`);
    item.license = metadata.license;
  }
}));

const inventory = {
  schema_version: 1,
  scope: "License metadata for every locked third-party JavaScript package, including optional platform packages and development dependencies. This is not a bundled-output clearance.",
  lockfiles: hashes,
  packages: [...packages.values()].sort((a, b) => a.name.localeCompare(b.name, "en") || a.version.localeCompare(b.version, "en")),
};
await writeFile(path.join(root, "licenses/javascript-packages.json"), JSON.stringify(inventory, null, 2) + "\n");
console.log(`Recorded license metadata for ${inventory.packages.length} package versions across ${lockfiles.length} lockfiles`);
