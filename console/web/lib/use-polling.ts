"use client";

import { useEffect } from "react";

import { startPolling } from "@/lib/polling";

/**
 * Polls with `startPolling` while `enabled`. Memoize `poll` (useCallback):
 * a new function restarts polling and aborts the previous one's signal.
 */
export function usePolling(
  poll: (signal: AbortSignal) => Promise<unknown> | void,
  intervalMs: number | null,
  { enabled = true, immediate = false }: { enabled?: boolean; immediate?: boolean } = {},
): void {
  useEffect(() => {
    if (!enabled) return;
    return startPolling(poll, intervalMs, { immediate });
  }, [enabled, immediate, intervalMs, poll]);
}
