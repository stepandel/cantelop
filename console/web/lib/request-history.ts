import type { RequestEvent } from "@/lib/telemetry-types";

// The request log is durable but paged: both the console route and the
// observer cap a page at 500 events. A table pages on demand, but a chart has
// to cover its whole window before it can be read, so the graph view walks
// backwards until the oldest loaded event predates the window — bounded, so a
// busy Workspace cannot turn one range change into an unbounded sweep.

// Backfill pulls full pages; the poll only needs whatever arrived in the last
// few seconds, and asking for 500 every 5s would be a large steady payload.
export const REQUEST_PAGE_SIZE = 500;
export const REQUEST_POLL_SIZE = 100;
export const MAX_HISTORY_PAGES = 6;

// Events are newest first, keyed by the sequence that is also the cursor.
export function mergeEvents(current: RequestEvent[], incoming: RequestEvent[]) {
  const events = new Map(current.map((event) => [event.sequence, event]));
  for (const event of incoming) events.set(event.sequence, event);
  return [...events.values()].sort((a, b) => b.sequence - a.sequence);
}

// Whether the loaded history already reaches back past the window's left edge.
// False means the earliest buckets are missing events rather than idle.
export function coversWindow(events: RequestEvent[], windowStart: number) {
  const oldest = events[events.length - 1];
  if (!oldest) return false;
  const at = Date.parse(oldest.received_at);
  return !Number.isNaN(at) && at <= windowStart;
}
