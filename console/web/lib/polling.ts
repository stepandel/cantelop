export interface PollingOptions {
  /** Poll as soon as polling starts instead of after the first interval. */
  immediate?: boolean;
}

/**
 * Calls `poll` every `intervalMs` while the page is visible. A hidden tab skips
 * its ticks and polls again the moment it becomes visible, and a tick never
 * starts while the previous call is still in flight. A null interval polls
 * only once, immediately. `poll` handles its own failures; its signal aborts
 * when polling stops, so a late response can be discarded. Returns `stop`.
 */
export function startPolling(
  poll: (signal: AbortSignal) => Promise<unknown> | void,
  intervalMs: number | null,
  { immediate = false }: PollingOptions = {},
): () => void {
  const controller = new AbortController();
  let inFlight = false;
  const tick = async () => {
    if (controller.signal.aborted || inFlight || document.hidden) return;
    inFlight = true;
    try {
      await poll(controller.signal);
    } finally {
      inFlight = false;
    }
  };
  const onVisible = () => {
    if (!document.hidden) void tick();
  };

  if (immediate || intervalMs === null) void tick();
  if (intervalMs === null) return () => controller.abort();
  const timer = window.setInterval(() => void tick(), intervalMs);
  document.addEventListener("visibilitychange", onVisible);
  return () => {
    controller.abort();
    window.clearInterval(timer);
    document.removeEventListener("visibilitychange", onVisible);
  };
}
