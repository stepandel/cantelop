import type { Snapshot } from "@/lib/telemetry-types";

// One usage observation for a Sandbox. CPU is percent of the sandbox's
// configured vCPU capacity and needs two consecutive cgroup readings, so the
// first sample of a session carries memory only.
export interface UsageSample {
  observed_at: string;
  cpu_percent?: number;
  memory_bytes?: number;
}

export type UsageHistory = Record<string, UsageSample[]>;

// The host retains no time series, so the console keeps a small rolling window
// per Sandbox: ~10 minutes at the 5-second poll interval.
export const MAX_SAMPLES = 120;

export function appendUsageSamples(
  history: UsageHistory,
  snapshot: Snapshot,
  cpuRates: Record<string, number>,
): UsageHistory {
  let changed = false;
  const next: UsageHistory = {};

  for (const instance of snapshot.sandboxes) {
    const samples = history[instance.id] ?? [];
    if (!instance.usage) {
      next[instance.id] = samples;
      continue;
    }
    const last = samples[samples.length - 1];
    if (last?.observed_at === instance.usage.observed_at) {
      next[instance.id] = samples;
      continue;
    }
    changed = true;
    next[instance.id] = [
      ...samples,
      {
        observed_at: instance.usage.observed_at,
        cpu_percent: cpuRates[instance.id],
        memory_bytes: instance.usage.guest_memory_used_bytes,
      },
    ].slice(-MAX_SAMPLES);
  }

  // Dropping records for Sandboxes no longer in the snapshot bounds growth.
  if (Object.keys(history).length !== Object.keys(next).length) changed = true;
  return changed ? next : history;
}
