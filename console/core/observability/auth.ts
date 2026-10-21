import { timingSafeEqual } from "node:crypto";

export function authenticateObservationIngest(
  authorization: string | null,
  environment: NodeJS.ProcessEnv = process.env,
): "edge" | "runtime" {
  const runtime = environment.CANTELOP_OBSERVABILITY_INGEST_TOKEN?.trim();
  const edge = environment.CANTELOP_EDGE_OBSERVABILITY_INGEST_TOKEN?.trim();
  if (!/^[0-9a-f]{64}$/.test(runtime ?? "") || !/^[0-9a-f]{64}$/.test(edge ?? "") ||
      runtime === edge || !authorization?.startsWith("Bearer ") || authorization.includes(",")) {
    throw new Error("invalid observability ingest credential");
  }
  const presented = authorization.slice("Bearer ".length);
  if (presented.length === edge!.length && timingSafeEqual(Buffer.from(presented), Buffer.from(edge!))) return "edge";
  if (presented.length === runtime!.length && timingSafeEqual(Buffer.from(presented), Buffer.from(runtime!))) return "runtime";
  throw new Error("invalid observability ingest credential");
}

export function authorizeObservationSources(
  producer: "edge" | "runtime",
  sources: readonly string[],
): void {
  if (sources.some((source) => producer === "edge" ? source !== "edge" : source === "edge")) {
    throw new Error("observability producer is not authorized for the event source");
  }
}
