import { platformInternalPort } from "@core/domain/model";

export const routingSlugPattern = /^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/;

export function validRoutingSlug(value: unknown): value is string {
  return typeof value === "string" && routingSlugPattern.test(value) && !value.includes("--");
}

export function customerWorkspaceSpec(input: unknown) {
  if (typeof input !== "object" || input === null || Array.isArray(input)) throw new Error("invalid Workspace settings");
  const parsed = input as Record<string, unknown>;
  if (Object.keys(parsed).some((key) => key !== "app_id" && key !== "slug") ||
      typeof parsed.app_id !== "string" || !/^app_[0-9a-f]{32}$/.test(parsed.app_id) ||
      !validRoutingSlug(parsed.slug)) {
    throw new Error("invalid Workspace settings");
  }
  return { appID: parsed.app_id, slug: parsed.slug };
}

export function customerAppSpec(input: unknown) {
  if (typeof input !== "object" || input === null || Array.isArray(input)) throw new Error("invalid App settings");
  const parsed = input as Record<string, unknown>;
  if (Object.keys(parsed).some((key) => key !== "app_slug") ||
      !validRoutingSlug(parsed.app_slug)) {
    throw new Error("invalid App settings");
  }
  return {
    slug: parsed.app_slug,
    sessionRuntime: {
      internalPort: platformInternalPort,
    },
  };
}
