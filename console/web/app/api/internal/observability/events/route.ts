import { after, NextRequest, NextResponse } from "next/server";

import { readControlJSON } from "@core/http/request";
import { authenticateObservationIngest, authorizeObservationSources } from "@core/observability/auth";
import { observationRepository } from "@core/observability/control";
import { parseObservationBatch } from "@core/observability/events";

import { ObservationRejectedError } from "@core/observability/repository";

const MAX_BODY_BYTES = 1 << 20;

export const dynamic = "force-dynamic";

export async function POST(request: NextRequest) {
  let producer: "edge" | "runtime";
  try {
    producer = authenticateObservationIngest(request.headers.get("authorization"));
  } catch {
    return NextResponse.json({ error: "authentication_required" }, { status: 401 });
  }

  let batch;
  try {
    batch = parseObservationBatch(await readControlJSON(request, MAX_BODY_BYTES));
    authorizeObservationSources(producer, batch.events.map(({ source }) => source));
  } catch {
    return NextResponse.json({ error: "invalid_observation_batch" }, { status: 400 });
  }

  const repository = observationRepository();
  let result;
  try {
    result = await repository.ingest(batch);
  } catch (error) {
    if (error instanceof ObservationRejectedError) {
      console.warn("rejected observability batch", error.message);
      return NextResponse.json({ error: "observation_rejected" }, { status: 422 });
    }
    console.error("could not ingest observability batch", error);
    return NextResponse.json({ error: "observability_unavailable" }, { status: 503 });
  }
  // Retention runs after the response, outside the ingestion transaction.
  after(() => repository.expireIfDue().catch((error: unknown) => {
    console.error("observability_retention_failed", error);
  }));
  return NextResponse.json(result, { status: 202 });
}
