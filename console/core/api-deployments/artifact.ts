import { appCommandPathPrefix, appCommandTypes } from "./commands";
import { canonical, parseProtocolEdgeManifest, type ProtocolEdgeManifest } from "./protocol-artifact";

export { appCommandPathPrefix, appCommandType, appCommandTypes, type AppCommandType } from "./commands";

export const apiRouteMethods = ["DELETE", "GET", "HEAD", "OPTIONS", "PATCH", "POST", "PUT"] as const;
export type APIRouteMethod = (typeof apiRouteMethods)[number];

export interface APIRoute {
  method: APIRouteMethod;
  path: string;
}

// The platform deploys only the Edge Worker SDK 1.0 generates from an App
// definition. Customer-authored API Workers (the pre-1.0 `cantelop-edge-api`
// manifests) are rejected; rows that predate 1.0 remain in history but are
// never parsed as deployable artifacts.
export type APIArtifactManifest = ProtocolEdgeManifest;

export function parseAPIArtifactManifest(value: unknown): APIArtifactManifest {
  if (!isSDKOneEdgeManifest(value)) throw new TypeError("invalid API artifact manifest");
  return parseProtocolEdgeManifest(value);
}

/** Whether a stored manifest came from SDK 1.0, without validating the rest of it. */
export function isSDKOneEdgeManifest(value: unknown): boolean {
  return typeof value === "object" && value !== null && !Array.isArray(value) &&
    (value as { kind?: unknown }).kind === "cantelop-protocol-edge";
}

export function validAPIArtifactManifest(manifest: APIArtifactManifest): boolean {
  try {
    return sameAPIArtifactManifest(parseAPIArtifactManifest(manifest), manifest);
  } catch {
    return false;
  }
}

export function sameAPIArtifactManifest(left: unknown, right: unknown): boolean {
  return canonical(left) === canonical(right);
}

/** The generated Edge Worker's ingress: one POST route per command (auth only
 *  when the App declares it), the homepage and the declared webhooks. */
export function apiArtifactRoutes(manifest: APIArtifactManifest): APIRoute[] {
  return [
    ...appCommandTypes.filter((type) => type !== "auth" || manifest.auth)
      .map((type): APIRoute => ({ method: "POST", path: `${appCommandPathPrefix}${type}` })),
    ...(manifest.homepage ? [{ method: "GET", path: "/" }, { method: "HEAD", path: "/" }] as APIRoute[] : []),
    ...manifest.webhooks,
  ].sort((a, b) => a.path.localeCompare(b.path) || a.method.localeCompare(b.method));
}
