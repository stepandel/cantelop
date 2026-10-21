import { formatElapsed } from "@/lib/format";
import type { AppBillingSummary } from "@/lib/instances";

export function billingTimeLabel(summary?: AppBillingSummary) {
  if (!summary) return "—";
  if (summary.unknown && summary.measured === 0) return "Incomplete";
  return `${summary.unknown ? "≥ " : ""}${formatElapsed(summary.durationMS)}`;
}

export function billingTimeTitle(summary?: AppBillingSummary) {
  if (!summary) return "Runtime timing unavailable";
  if (summary.unknown) return `Incomplete history: ${summary.unknown} sandbox${summary.unknown === 1 ? " has" : "es have"} unknown uptime and are excluded from this total.`;
  return "Measured sandbox uptime, including idle time while running.";
}
