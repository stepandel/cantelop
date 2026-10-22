import { createHash } from "node:crypto";
import { lstatSync, readFileSync, readdirSync } from "node:fs";
import { dirname, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "../..");
const digest = (data: string | Buffer) => createHash("sha256").update(data).digest("hex");

/** Verify before migrations, including on Vercel checkouts without .git. */
export function verifyPublicSource(directory = root, vercel = process.env.VERCEL === "1"): void {
  const pin = JSON.parse(readFileSync(join(directory, "console/public-source.json"), "utf8"));
  if (pin.visibility !== undefined && !["public", "private"].includes(pin.visibility)) throw new Error("invalid source visibility");
  if (pin.commit === null && pin.snapshot_sha256 === null) return;
  if (pin.schema_version !== 1 || !/^[0-9a-f]{40}$/.test(pin.commit) || !/^[0-9a-f]{64}$/.test(pin.snapshot_sha256)) throw new Error("invalid public source pin");
  const manifestBytes = readFileSync(join(directory, "SOURCE_SNAPSHOT.json"));
  if (digest(manifestBytes) !== pin.snapshot_sha256) throw new Error("public source manifest digest does not match the pin");
  const manifest = JSON.parse(manifestBytes.toString());
  if (manifest.schema_version !== 1 || !manifest.files || !Object.keys(manifest.files).length) throw new Error("invalid public source manifest");
  const policy = JSON.parse(readFileSync(join(directory, ".public-source-policy.json"), "utf8"));
  const pattern = (glob: string) => new RegExp(`^${glob.split("*").map((part) => part.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")).join(".*")}$`);
  const ignoredPatterns = policy.ignored_file_patterns.map(pattern);
  const names: string[] = [];
  function visit(folder: string): void {
    for (const entry of readdirSync(folder, { withFileTypes: true })) {
      const path = join(folder, entry.name);
      const name = relative(directory, path).split("\\").join("/");
      if (policy.private_files.includes(name) || policy.ignored_files.includes(name) || policy.private_prefixes.some((prefix: string) => `${name}/`.startsWith(prefix)) || ignoredPatterns.some((regex: RegExp) => regex.test(entry.name))) continue;
      if (entry.isDirectory()) {
        if (!policy.ignored_directories.includes(entry.name)) visit(path);
      } else {
        if (!entry.isFile()) throw new Error(`publication requires a regular source file: ${name}`);
        // Vercel generates root configuration from project/account settings.
        // It is a build input from the provider; console/vercel.json remains
        // versioned source and is checked against the publication manifest.
        if (vercel && name === "vercel.json" && !Object.hasOwn(manifest.files, name)) continue;
        if (name !== "SOURCE_SNAPSHOT.json") names.push(name);
      }
    }
  }
  visit(directory);
  const expectedNames = Object.keys(manifest.files);
  if (JSON.stringify(names.sort()) !== JSON.stringify(expectedNames.sort())) {
    const added = names.filter((name) => !Object.hasOwn(manifest.files, name));
    const present = new Set(names);
    const missing = expectedNames.filter((name) => !present.has(name));
    throw new Error(`source file list changed since publication; added: ${added.slice(0, 10).join(", ") || "none"}; missing: ${missing.slice(0, 10).join(", ") || "none"}; publish and pin a new snapshot`);
  }
  for (const name of names) {
    const path = join(directory, name);
    let data: Buffer | string = readFileSync(path);
    if (name === "console/public-source.json") data = JSON.stringify({ schema_version: 1, repository: pin.repository, commit: null, snapshot_sha256: null }, null, 2) + "\n";
    if (name === ".gitleaksignore") data = "# Public snapshots use exact fixture allowlists in .gitleaks.toml.\n";
    const expected = manifest.files[name];
    const mode = lstatSync(path).mode & 0o111 ? 0o755 : 0o644;
    if (digest(data) !== expected.sha256 || mode !== expected.mode) throw new Error(`source changed since publication: ${name}; publish and pin a new snapshot`);
  }
}

if (import.meta.main) verifyPublicSource();
