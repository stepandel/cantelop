"use client";

import { useCallback, useState } from "react";

import { CopyId } from "@/components/copy-id";
import type { PlatformDiagnosticEvent } from "@/lib/platform-diagnostics";
import { useClock } from "@/lib/use-clock";
import { usePolling } from "@/lib/use-polling";

const POLL_INTERVAL_MS = 5_000;

export function PlatformDiagnosticsPanel({ active, polling = true }: { active: boolean; polling?: boolean }) {
  const [events, setEvents] = useState<PlatformDiagnosticEvent[] | null>(null);
  const [unavailable, setUnavailable] = useState(false);
  const { formatDate, formatStamp } = useClock();

  const refresh = useCallback(async (signal: AbortSignal) => {
    try {
      const response = await fetch("/api/admin/diagnostics", { cache: "no-store" });
      const page = await response.json() as { items?: PlatformDiagnosticEvent[] };
      if (!response.ok || !Array.isArray(page.items)) throw new Error("diagnostics unavailable");
      if (!signal.aborted) {
        setEvents(page.items);
        setUnavailable(false);
      }
    } catch {
      if (!signal.aborted) setUnavailable(true);
    }
  }, []);
  // Without polling the panel still loads once when it becomes active.
  usePolling(refresh, polling ? POLL_INTERVAL_MS : null, { enabled: active, immediate: true });

  return (
    <>
      <p className="table-note">Pre-workspace edge failures from the last 6h · operator only</p>
      {unavailable && (
        <div className="notice error" role="alert">
          Infrastructure diagnostics are unavailable{events ? " — showing the last successful result" : ""}
        </div>
      )}
      {events === null && !unavailable ? (
        <p className="empty-note">Loading infrastructure diagnostics…</p>
      ) : events?.length === 0 ? (
        <p className="empty-note">No pre-workspace edge failures in the last 6h</p>
      ) : events ? (
        <table className="data-table">
          <colgroup>
            <col className="c-stamp" />
            <col className="c-kind" />
            <col className="c-num" />
            <col className="c-state" />
            <col />
            <col />
            <col />
          </colgroup>
          <thead>
            <tr><th>Received</th><th>Stage</th><th className="num">Status</th><th>Method</th><th>Target</th><th>Reason</th><th>Request</th></tr>
          </thead>
          <tbody>
            {events.map((event) => (
              <tr key={`${event.timestamp}:${event.request_id}`}>
                <td className="sub" title={formatDate(event.timestamp)}>{formatStamp(event.timestamp)}</td>
                <td className={event.outcome === "failed" ? "bad" : "warn"}>{event.stage}</td>
                <td className={`num http-${Math.floor(event.status / 100)}xx`}>{event.status}</td>
                <td>{event.method}</td>
                <td className="dim ellip" title={`${event.hostname}${event.path}`}>{event.hostname}{event.path}</td>
                <td className="dim">{event.reason}</td>
                <td className="dim ellip"><CopyId value={event.request_id} /></td>
              </tr>
            ))}
          </tbody>
        </table>
      ) : null}
    </>
  );
}
