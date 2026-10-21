// What a release says about the App it deploys, read back from the manifests
// the SDK 1.0 compiler generated. Releases recorded before SDK 1.0 have no App
// definition; the summary is absent for them and they cannot be reactivated.
//
// Manifests were validated when they were uploaded; this only projects them for
// display and tolerates fields it does not know rather than rejecting a release
// the platform already accepted.

export interface AppDefinitionSummary {
  app_name: string;
  session_runtime_id: string;
  /** Ingress the generated Edge Worker serves besides its POST /commands/{type} paths. */
  webhooks: { path: string; method: "GET" | "POST" }[];
  homepage: boolean;
  /** The App declares a native CLI sign-in provider (`auth` in cantelop.app). */
  native_auth: boolean;
  /** The Edge accepts a rotated-out integration token until it expires. */
  token_rotation: boolean;
  dockerfile?: string;
  /** Null when the Session runtime manifest was not recorded. */
  capabilities: {
    steering: boolean;
    message_cancellation: boolean;
    durable_view: boolean;
  } | null;
}

export function appDefinitionSummary(apiManifest: unknown, runtimeManifest: unknown): AppDefinitionSummary | undefined {
  if (!record(apiManifest) || apiManifest.kind !== "cantelop-protocol-edge") return undefined;
  if (typeof apiManifest.app_name !== "string" || typeof apiManifest.session_runtime_id !== "string") return undefined;
  const webhooks = Array.isArray(apiManifest.webhooks)
    ? apiManifest.webhooks.flatMap((hook) =>
      record(hook) && typeof hook.path === "string" && (hook.method === "GET" || hook.method === "POST")
        ? [{ path: hook.path, method: hook.method as "GET" | "POST" }]
        : [])
    : [];
  const capabilities = record(runtimeManifest) && record(runtimeManifest.capabilities) ? runtimeManifest.capabilities : undefined;
  return {
    app_name: apiManifest.app_name,
    session_runtime_id: apiManifest.session_runtime_id,
    webhooks,
    homepage: apiManifest.homepage === true,
    native_auth: apiManifest.auth === true,
    token_rotation: Array.isArray(apiManifest.optional_bindings),
    ...(typeof apiManifest.dockerfile === "string" ? { dockerfile: apiManifest.dockerfile } : {}),
    capabilities: capabilities === undefined ? null : {
      steering: capabilities.steering === true,
      message_cancellation: capabilities.messageCancellation === true,
      durable_view: capabilities.durableView === true,
    },
  };
}

function record(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}
