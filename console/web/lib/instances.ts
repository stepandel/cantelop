import type { Sandbox, SandboxState, Snapshot } from "@/lib/telemetry-types";

export const SANDBOX_STATE_ORDER: SandboxState[] = ["running", "starting", "stopping", "failed", "terminated"];

// CPU is a rate the host does not report: each snapshot carries a cumulative
// cgroup reading, so a percentage needs the delta between two consecutive
// observations of the same Sandbox.
export function calculateCPURates(previous: Snapshot | null, next: Snapshot) {
  const rates: Record<string, number> = {};
  const before = new Map((previous?.sandboxes ?? []).map((sandbox) => [sandbox.id, sandbox]));
  for (const instance of next.sandboxes) {
    const prior = before.get(instance.id);
    if (!instance.usage || !prior?.usage || instance.vcpus < 1) continue;
    const elapsedUS = (Date.parse(instance.usage.observed_at) - Date.parse(prior.usage.observed_at)) * 1000;
    const consumedUS = instance.usage.cpu_usage_us - prior.usage.cpu_usage_us;
    if (elapsedUS <= 0 || consumedUS < 0) continue;
    const percentage = (consumedUS / elapsedUS / instance.vcpus) * 100;
    if (Number.isFinite(percentage)) rates[instance.id] = Math.max(0, percentage);
  }
  return rates;
}

const TERMINAL_SANDBOX_STATES = new Set<SandboxState>(["terminated", "failed"]);

// A record that provably never had a process: the runtime recorded
// not_started, or it is still waiting for its first start.
export function isVerifiedZeroRuntime(sandbox: Sandbox) {
  const status = sandbox.runtime_timing?.status;
  return status === "not_started" || (status === "pending" && sandbox.state === "starting");
}

// Only measured process intervals are evidence of uptime. Lifecycle timestamps
// can change during late cleanup and must never be used as a fallback. An open
// interval on a terminal record is a lost exit write, not a live process.
export function sandboxRuntimeInterval(sandbox: Sandbox, observedAt: string) {
  const timing = sandbox.runtime_timing;
  if (!timing || (timing.status !== "running" && timing.status !== "stopped")) return undefined;
  const terminal = timing.status === "stopped";
  if (!terminal && TERMINAL_SANDBOX_STATES.has(sandbox.state)) return undefined;
  const start = Date.parse(timing.started_at ?? "");
  const end = Date.parse(terminal ? timing.stopped_at ?? "" : observedAt);
  if (!Number.isFinite(start) || !Number.isFinite(end) || end < start) return undefined;
  return { start, end, terminal };
}

export function sandboxRuntimeDuration(sandbox: Sandbox, observedAt: string) {
  if (isVerifiedZeroRuntime(sandbox)) return 0;
  const interval = sandboxRuntimeInterval(sandbox, observedAt);
  return interval ? interval.end - interval.start : undefined;
}

export interface AppBillingSummary {
  durationMS: number;
  measured: number;
  unknown: number;
}

export function appBillingSummary(snapshot: Snapshot | null, appId: string): AppBillingSummary | undefined {
  if (!snapshot) return undefined;
  const result = { durationMS: 0, measured: 0, unknown: 0 };
  for (const sandbox of snapshot.sandboxes) {
    if (sandbox.app_id !== appId) continue;
    const duration = sandboxRuntimeDuration(sandbox, snapshot.observed_at);
    if (duration === undefined) result.unknown += 1;
    else { result.durationMS += duration; result.measured += 1; }
  }
  return result;
}

/** Running Sandboxes per App id. */
export function runningByApp(sandboxes: Sandbox[]): Map<string, number> {
  const running = new Map<string, number>();
  for (const sandbox of sandboxes) {
    if (sandbox.state === "running") running.set(sandbox.app_id, (running.get(sandbox.app_id) ?? 0) + 1);
  }
  return running;
}

export function sortSandboxes(sandboxes: Sandbox[]) {
  return [...sandboxes].sort((a, b) => (Date.parse(b.created_at) || 0) - (Date.parse(a.created_at) || 0));
}
