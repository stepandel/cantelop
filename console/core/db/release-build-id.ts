import type { ProductRelease } from "./release-repository";

/** The host Build a release's Session runtime materialized, if it has one. */
export function releaseBuildID(release: ProductRelease): string | null {
  if (release.components.session_runtime.status !== "succeeded") return null;
  const output = release.components.session_runtime.output;
  const build = output?.build_id ?? output?.provider_build_id;
  return typeof build === "string" && /^sha256:[0-9a-f]{64}$/.test(build) ? build : null;
}
