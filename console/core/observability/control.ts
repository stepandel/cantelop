import { observabilityDatabase, observabilityRetentionHours } from "./database";
import { ObservationRepository } from "./repository";

let shared: ObservationRepository | undefined;

export function observationRepository(): ObservationRepository {
  if (!shared) shared = ObservationRepository.fromPool(observabilityDatabase(), observabilityRetentionHours());
  return shared;
}
