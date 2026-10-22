import { assertResourceID } from "./ids";

export const appSlugPattern = /^(?!.*--)[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/;
const reservedAppSlugs = new Set(["console", "edge-origin", "release-gate", "www"]);
export const identityProviderPattern = /^[a-z][a-z0-9_-]{0,31}$/;
export const identitySubjectPattern = /^[A-Za-z0-9][A-Za-z0-9:_-]{0,254}$/;
// Private-use port injected into the isolated Session runtime; it is never bound on the host.
export const platformInternalPort = 57321;

// Each list is the single source for its type, the drizzle column type and the
// table's CHECK constraint. The order is the CHECK's order; reordering a list
// changes the constraint text and would generate a migration.
export const userStatuses = ["active", "suspended"] as const;
export const appStatuses = ["provisioning", "active", "archived"] as const;
export const operationStatuses = ["queued", "running", "succeeded", "failed"] as const;
export const operationKinds = [
  "register_app", "register_workspace", "archive_workspace", "purge_workspace", "sync_config",
  "deploy_api", "materialize_build", "activate_release", "archive_app", "cancel_release", "attach_package",
] as const;
export const releaseStatuses = ["queued", "provisioning", "cancelling", "cancelled", "active", "failed", "superseded"] as const;
export const releaseComponentKinds = ["api", "session_runtime"] as const;
export const releaseComponentStatuses = ["queued", "running", "succeeded", "failed"] as const;

export type UserStatus = typeof userStatuses[number];
export type AppStatus = typeof appStatuses[number];
export type OperationStatus = typeof operationStatuses[number];
export type OperationKind = typeof operationKinds[number];
export type ReleaseStatus = typeof releaseStatuses[number];
export type ReleaseComponentKind = typeof releaseComponentKinds[number];
export type ReleaseComponentStatus = typeof releaseComponentStatuses[number];

export interface IdentityInput {
  provider: string;
  providerSubject: string;
  email: string;
}

export interface SessionRuntimeSpec {
  internalPort: number;
}

export interface CreateAppInput {
  slug: string;
  sessionRuntime: SessionRuntimeSpec;
}

export interface CreateWorkspaceInput {
  appID: string;
  slug: string;
}

export interface UserQuota {
  maxApps: number;
}

/** Server-side App-slot accounting: each standalone App and each Fleet. */
export interface UserQuotaUsage extends UserQuota {
  usedApps: number;
}

export const defaultUserQuota: Readonly<UserQuota> = Object.freeze({
  maxApps: 5,
});

/** Folds only ASCII letters, matching Postgres lower() under the "C"
 *  collation the email checks use, so both sides agree on every address. */
export function normalizeEmail(email: string): string {
  return email.replace(/[A-Z]+/g, (letters) => letters.toLowerCase());
}

export function validateIdentity(input: IdentityInput): IdentityInput {
  const email = normalizeEmail(input.email);
  if (!identityProviderPattern.test(input.provider) ||
      !identitySubjectPattern.test(input.providerSubject) ||
      email.trim() !== email || email.length === 0 || email.length > 320 || email.includes("\0")) {
    throw new TypeError("invalid identity");
  }
  return Object.freeze({ ...input, email });
}

export function validateSessionRuntime(input: SessionRuntimeSpec): SessionRuntimeSpec {
  if (!Number.isInteger(input.internalPort) || input.internalPort < 1 || input.internalPort > 65535) {
    throw new TypeError("invalid Session runtime");
  }
  return Object.freeze({ ...input });
}

export function validateCreateApp(input: CreateAppInput): CreateAppInput {
  if (!appSlugPattern.test(input.slug) || reservedAppSlugs.has(input.slug)) {
    throw new TypeError("invalid App routing slug");
  }
  return Object.freeze({ ...input, sessionRuntime: validateSessionRuntime(input.sessionRuntime) });
}

export function validateCreateWorkspace(input: CreateWorkspaceInput): CreateWorkspaceInput {
  assertResourceID("app", input.appID);
  if (!appSlugPattern.test(input.slug)) throw new TypeError("invalid Workspace slug");
  return Object.freeze({ ...input });
}
