import type { AppTraceTree, TraceRequest } from "@core/observability/trace-tree";

const TRACE_POLL_MS = 1_500;
const TRACE_POLL_LIMIT = 40;

/** Watch one exchange. Cleanup cancels both scheduled and in-flight polls. */
export function watchRequestTrace(
  appId: string,
  requestId: string,
  onTrace: (trace: TraceRequest | null, done: boolean) => void,
): () => void {
  const controller = new AbortController();
  let timer: ReturnType<typeof setTimeout>;
  const watch = (attempt: number) => {
    timer = setTimeout(async () => {
      let trace: TraceRequest | null = null;
      try {
        const response = await fetch(`/api/customer/apps/${appId}/requests/${requestId}/trace`, {
          cache: "no-store",
          signal: controller.signal,
        });
        if (response.ok) trace = ((await response.json()) as AppTraceTree).requests[0] ?? null;
      } catch { /* try again unless cancelled */ }
      // Fetch or body parsing may finish after cleanup, even if abort was ignored.
      if (controller.signal.aborted) return;
      const settled = trace !== null && trace.messages.length > 0 &&
        trace.messages.every((message) => message.attempts.length > 0 && message.attempts.every((attempt) => attempt.outcome !== "pending"));
      const quiet = trace !== null && trace.messages.length === 0 && attempt >= 3;
      const done = settled || quiet || attempt + 1 >= TRACE_POLL_LIMIT;
      onTrace(trace, done);
      if (!done && !controller.signal.aborted) watch(attempt + 1);
    }, attempt === 0 ? 400 : TRACE_POLL_MS);
  };
  watch(0);
  return () => {
    controller.abort();
    clearTimeout(timer);
  };
}
