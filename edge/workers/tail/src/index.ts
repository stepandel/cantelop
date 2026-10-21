import { publishObservations, type ObservationIngestEnv } from "@cantelop/edge-contracts";

import { observationsFromTail } from "./core";

export default {
  async tail(events: TraceItem[], environment: ObservationIngestEnv): Promise<void> {
    const observations = await observationsFromTail(events);
    await publishObservations(environment, observations, { userAgent: "cantelop-edge-tail/1" });
  },
};
