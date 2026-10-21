import { randomBytes } from "node:crypto";

export const idPatterns = Object.freeze({
  user: /^usr_[0-9a-f]{32}$/,
  app: /^app_[0-9a-f]{32}$/,
  workspace: /^wsp_[0-9a-f]{32}$/,
  operation: /^op_[0-9a-f]{32}$/,
  artifact: /^art_[0-9a-f]{32}$/,
  sessionRuntimeArtifact: /^sra_[0-9a-f]{32}$/,
  release: /^rel_[0-9a-f]{32}$/,
  deploymentUpload: /^upl_[0-9a-f]{32}$/,
});

export type ResourceKind = keyof typeof idPatterns;

const prefixes: Record<ResourceKind, string> = {
  user: "usr_",
  app: "app_",
  workspace: "wsp_",
  operation: "op_",
  artifact: "art_",
  sessionRuntimeArtifact: "sra_",
  release: "rel_",
  deploymentUpload: "upl_",
};

export function newResourceID(kind: ResourceKind): string {
  return `${prefixes[kind]}${randomBytes(16).toString("hex")}`;
}

export function assertResourceID(kind: ResourceKind, value: string): void {
  if (!idPatterns[kind].test(value)) throw new TypeError(`invalid ${kind} ID`);
}
