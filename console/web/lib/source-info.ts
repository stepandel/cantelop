import publishedSource from "../../public-source.json";

export interface Publication {
  visibility?: string;
  schema_version: number;
  repository: string;
  commit: string | null;
  snapshot_sha256: string | null;
}

export interface SourceInfo {
  private?: boolean;
  repository: string;
  commit: string;
  sourceURL: string;
}

/** Identify this deployment, rather than linking users to a moving main branch. */
export function sourceInfo(environment: NodeJS.ProcessEnv = process.env, publication: Publication | null = publishedSource): SourceInfo | null {
  if (publication?.visibility !== undefined && !["public", "private"].includes(publication.visibility)) throw new Error("invalid source visibility");
  const privateSource = publication?.visibility === "private" && !environment.CANTELOP_SOURCE_REPOSITORY?.trim();
  // An unpublished preview must not claim the last production snapshot as its
  // own source. Private deployments and configured repositories use their Git revision.
  if (environment.VERCEL_ENV?.trim() === "preview" && !privateSource && !environment.CANTELOP_SOURCE_REPOSITORY?.trim()) return null;
  // Public mappings require snapshot verification. Private pins only supply the
  // repository; their saved revision can be older than this deployment.
  const mapped = publication?.visibility !== "private" && publication?.commit && !environment.CANTELOP_SOURCE_COMMIT?.trim() && !environment.CANTELOP_SOURCE_REPOSITORY?.trim();
  if (mapped && (publication.schema_version !== 1 || !/^[0-9a-f]{64}$/.test(publication.snapshot_sha256 ?? ""))) throw new Error("invalid public source pin");
  const configured = environment.CANTELOP_SOURCE_COMMIT?.trim();
  const deployed = environment.VERCEL_GIT_COMMIT_SHA?.trim();
  if (!mapped && configured && deployed && configured !== deployed) throw new Error("source revision does not match the Vercel deployment");
  const commit = mapped ? publication.commit : configured || deployed;
  if (!commit) return null;
  if (!/^[0-9a-f]{40}$/.test(commit)) throw new Error("source commit must be a full lowercase Git commit");
  // Match scripts/release-notices.py: strip the slash and then ".git" from the configured text, and validate what remains.
  const base = (mapped || privateSource ? publication.repository : environment.CANTELOP_SOURCE_REPOSITORY?.trim() || "https://github.com/stepandel/cantelop").replace(/\/+$/, "").replace(/\.git$/, "");
  let repository: URL | null = null;
  try { repository = new URL(base); } catch {}
  if (!repository || !/^https:\/\//i.test(base) || !repository.hostname || repository.username || repository.password || base.includes("?") || base.includes("#") || !repository.pathname.replace(/\//g, "")) {
    throw new Error("source repository must be an HTTPS repository URL without credentials, query, or fragment");
  }
  return { repository: base, commit, sourceURL: `${base}/tree/${commit}`, ...(privateSource ? { private: true } : {}) };
}
