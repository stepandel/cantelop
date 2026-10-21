// Edge transport for typed observations sent to central observability.
// Console ingests at most 256 events and 1 MiB per request.

export const OBSERVABILITY_INGEST_PATH = "/api/internal/observability/events";
export const MAX_OBSERVATION_BATCH_EVENTS = 256;
export const MAX_OBSERVATION_BATCH_BYTES = 1 << 20;

const INGEST_TOKEN_PATTERN = /^[0-9a-f]{64}$/;
const BATCH_PREFIX = '{"events":[';
const BATCH_SUFFIX = "]}";
const encoder = new TextEncoder();
const EMPTY_BATCH_BYTES = encoder.encode(BATCH_PREFIX + BATCH_SUFFIX).length;

export interface ObservationIngestEnv {
  /** HTTPS origin of the Console observability ingestion endpoint. */
  OBSERVABILITY_INGEST_ORIGIN: string;
  OBSERVABILITY_INGEST_TOKEN: string;
}

export interface PublishObservationsOptions {
  userAgent: string;
  request?: typeof fetch;
}

/**
 * Sends observations in as few requests as the ingestion limits allow,
 * preserving their order. Nothing is sent for an empty list.
 */
export async function publishObservations(
  environment: ObservationIngestEnv,
  observations: readonly unknown[],
  { userAgent, request = fetch }: PublishObservationsOptions,
): Promise<void> {
  if (observations.length === 0) return;
  if (!INGEST_TOKEN_PATTERN.test(environment.OBSERVABILITY_INGEST_TOKEN)) {
    throw new Error("invalid observability ingestion credential");
  }
  const target = ingestURL(environment.OBSERVABILITY_INGEST_ORIGIN);
  let batch: string[] = [];
  let bytes = EMPTY_BATCH_BYTES;
  async function flush() {
    if (batch.length === 0) return;
    const response = await request(target, {
      method: "POST",
      headers: {
        Authorization: `Bearer ${environment.OBSERVABILITY_INGEST_TOKEN}`,
        "Content-Type": "application/json",
        "User-Agent": userAgent,
      },
      body: BATCH_PREFIX + batch.join(",") + BATCH_SUFFIX,
    });
    response.body?.cancel();
    if (!response.ok) {
      throw new Error(`observability ingestion failed with HTTP ${response.status}`);
    }
    batch = [];
    bytes = EMPTY_BATCH_BYTES;
  }
  for (const observation of observations) {
    const encoded = JSON.stringify(observation);
    const eventBytes = encoder.encode(encoded).length;
    if (batch.length === MAX_OBSERVATION_BATCH_EVENTS ||
        (batch.length > 0 && bytes + eventBytes + 1 > MAX_OBSERVATION_BATCH_BYTES)) {
      await flush();
    }
    if (eventBytes + bytes > MAX_OBSERVATION_BATCH_BYTES) {
      throw new Error("single observability event exceeds ingestion limit");
    }
    bytes += eventBytes + (batch.length === 0 ? 0 : 1);
    batch.push(encoded);
  }
  await flush();
}

function ingestURL(origin: string): string {
  let url: URL;
  try {
    url = new URL(origin);
  } catch {
    throw new Error("invalid observability ingestion origin");
  }
  if (url.protocol !== "https:" || url.username || url.password || url.search || url.hash ||
      url.pathname !== "/" || origin.endsWith("?") || origin.endsWith("#")) {
    throw new Error("invalid observability ingestion origin");
  }
  return new URL(OBSERVABILITY_INGEST_PATH, url).toString();
}
