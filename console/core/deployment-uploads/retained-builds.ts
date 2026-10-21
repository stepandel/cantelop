/**
 * Build IDs the Fire Fuse host still retains for an App, read through the
 * observer. Returns null when no observer is configured or it cannot answer,
 * so a rollback falls back to the activation worker's own check.
 */
export async function observedRetainedBuilds(
  appID: string,
  environment: NodeJS.ProcessEnv = process.env,
  request: typeof fetch = fetch,
): Promise<Set<string> | null> {
  const raw = environment.FIRE_FUSE_OBSERVER_URL;
  const token = environment.FIRE_FUSE_STATUS_TOKEN;
  if (!raw || !token || token.length < 32 || token.length > 512 || token.trim() !== token) return null;
  let origin: URL;
  try { origin = new URL(raw); } catch { return null; }
  const local = origin.protocol === "http:" && ["localhost", "127.0.0.1", "[::1]"].includes(origin.hostname);
  if ((!local && origin.protocol !== "https:") || origin.pathname !== "/" || origin.username || origin.password || origin.search || origin.hash) return null;
  let response: Response;
  try {
    response = await request(`${origin.origin}/apps/${encodeURIComponent(appID)}/builds`, {
      redirect: "error", headers: { Authorization: `Bearer ${token}` }, cache: "no-store", signal: AbortSignal.timeout(5000),
    });
  } catch { return null; }
  if (!response.ok && response.status !== 404) return null;
  let document: unknown;
  try { document = await response.json(); } catch { return null; }
  if (typeof document !== "object" || document === null || (document as { app_id?: unknown }).app_id !== appID) return null;
  // A proxy or an older observer may return 404 for an unsupported route.
  // Only explicit App absence means the host retains none of its Builds.
  if (response.status === 404) {
    const error = (document as { error?: unknown }).error;
    return typeof error === "object" && error !== null && (error as { code?: unknown }).code === "app_not_found" ? new Set() : null;
  }
  const builds = (document as { builds?: unknown }).builds;
  if (!Array.isArray(builds)) return null;
  const ids = new Set<string>();
  for (const build of builds) {
    const id = typeof build === "object" && build !== null ? (build as { id?: unknown }).id : undefined;
    if (typeof id !== "string" || !/^sha256:[0-9a-f]{64}$/.test(id)) return null;
    ids.add(id);
  }
  return ids;
}
