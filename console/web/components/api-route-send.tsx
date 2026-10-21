"use client";

import Link from "next/link";
import { useEffect, useState, type FormEvent } from "react";

import { RequestNode } from "@/components/trace-explorer";
import { watchRequestTrace } from "@/lib/request-trace-polling";
import type { RelayResponse } from "@core/api-routes/relay";
import type { TraceRequest } from "@core/observability/trace-tree";

import styles from "./api-route-send.module.css";

const METHODS_WITH_BODY = new Set(["PATCH", "POST", "PUT", "DELETE"]);

type Sent =
  | { state: "sending" }
  | { state: "failed"; error: string }
  | { state: "answered"; response: RelayResponse; trace: TraceRequest | null; tracePolling: boolean };

/** Sends one request to this route from the console and shows what came
 *  back beside what the request set in motion: its trace, filling in as the
 *  Session works, so a test of an async route shows the outcome and not
 *  only the 202 that accepted it. */
export function ApiRouteSend({
  appId,
  method,
  path,
  origin,
  declared,
  tracesHrefBase,
  initialSent,
}: {
  appId: string;
  method: string;
  path: string;
  origin: string;
  declared: boolean;
  tracesHrefBase: string;
  // Previews render a finished exchange.
  initialSent?: Sent;
}) {
  const takesBody = METHODS_WITH_BODY.has(method);
  const [query, setQuery] = useState("");
  const [headers, setHeaders] = useState("");
  const [body, setBody] = useState(takesBody ? "{}" : "");
  const [sent, setSent] = useState<Sent | undefined>(initialSent);
  const trimmedQuery = query.trim().replace(/^\?/, "");
  const queryPart = trimmedQuery === "" ? "" : `?${trimmedQuery}`;
  const requestId = sent?.state === "answered" && sent.tracePolling ? sent.response.request_id : undefined;

  useEffect(() => {
    if (requestId === undefined) return;
    return watchRequestTrace(appId, requestId, (trace, done) => {
      setSent((current) => current?.state === "answered" && current.response.request_id === requestId
        ? { ...current, trace: trace ?? current.trace, tracePolling: !done }
        : current);
    });
  }, [appId, requestId]);

  const send = async (event: FormEvent) => {
    event.preventDefault();
    let parsedHeaders: Record<string, string>;
    try {
      parsedHeaders = parseHeaders(headers);
    } catch (error) {
      setSent({ state: "failed", error: error instanceof Error ? error.message : "Headers are invalid" });
      return;
    }
    setSent({ state: "sending" });
    try {
      const response = await fetch(`/api/customer/apps/${appId}/routes/send`, {
        method: "POST",
        headers: { "content-type": "application/json" },
        cache: "no-store",
        body: JSON.stringify({
          method,
          path,
          ...(trimmedQuery === "" ? {} : { query: trimmedQuery }),
          ...(Object.keys(parsedHeaders).length === 0 ? {} : { headers: parsedHeaders }),
          ...(takesBody && body !== "" ? { body } : {}),
        }),
      });
      if (!response.ok) {
        const failure = await response.json().catch(() => ({})) as { error?: string };
        setSent({ state: "failed", error: describeFailure(response.status, failure.error) });
        return;
      }
      const relayed = await response.json() as RelayResponse;
      setSent({ state: "answered", response: relayed, trace: null, tracePolling: relayed.request_id !== undefined });
    } catch {
      setSent({ state: "failed", error: "The console could not send the request" });
    }
  };

  return (
    <>
      <h2 className="section-label">Send a request</h2>
      <div className="section-panel">
        {!declared ? (
          <p className="empty-note">Not declared by the live build</p>
        ) : (
          <form className={`inline-form ${styles.sendForm}${takesBody ? ` ${styles.hasBody}` : ""}`} onSubmit={(event) => void send(event)}>
            <div className={styles.sendLine}>
              <span className={styles.sendMethod}>{method}</span>
              <code className={styles.sendUrl} title={`${origin}${path}${queryPart}`}>
                <span className={styles.sendOrigin}>{origin}</span>{path}<span className={styles.sendQuery}>{queryPart}</span>
              </code>
              <button type="submit" disabled={sent?.state === "sending"}>{sent?.state === "sending" ? "Sending…" : "Send"}</button>
            </div>
            <div className={styles.sendFields}>
              <label className={styles.sendQueryField}>
                Query string
                <input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="workspace=default" spellCheck={false} />
              </label>
              <label className={styles.sendHeaders}>
                <span>Headers <em>one per line</em></span>
                <textarea value={headers} onChange={(event) => setHeaders(event.target.value)} placeholder="Authorization: Bearer …" rows={takesBody ? 4 : 2} spellCheck={false} />
              </label>
              {takesBody && (
                <label className={styles.sendBody}>
                  Body
                  <textarea value={body} onChange={(event) => setBody(event.target.value)} rows={8} spellCheck={false} />
                </label>
              )}
            </div>
          </form>
        )}
        {sent?.state === "failed" && <div className="notice error" role="alert">{sent.error}</div>}
        {sent?.state === "answered" && (
          <>
            <div className="monitor-head">
              <strong className={`${styles.sendStatus} http-${Math.floor(sent.response.status / 100)}xx`}>{sent.response.status}</strong>
              <dl className="monitor-meta"><dt>Took</dt><dd>{sent.response.duration_ms}ms</dd></dl>
              {sent.response.request_id && <dl className="monitor-meta"><dt>Request</dt><dd><code>{sent.response.request_id}</code></dd></dl>}
              {sent.response.body_truncated && <dl className="monitor-meta"><dt>Body</dt><dd>Truncated at 1 MiB</dd></dl>}
              {sent.response.request_id && (
                <Link className="monitor-close" href={`${tracesHrefBase}${encodeURIComponent(path)}&request=${encodeURIComponent(sent.response.request_id)}#${sent.response.request_id}`}>Open in Traces</Link>
              )}
            </div>
            <div className={styles.sendExchange}>
              <section>
                <div className="section-label">Response headers</div>
                <pre className="code-well">{Object.entries(sent.response.headers).map(([name, value]) => `${name}: ${value}`).join("\n") || "—"}</pre>
                <div className="section-label">Response body</div>
                <pre className="code-well">{prettyBody(sent.response.body, sent.response.headers["content-type"]) || "(empty)"}</pre>
              </section>
              <section>
                <div className="section-label">
                  Trace
                  {sent.tracePolling && <span className="spinner send-spinner" aria-label="Watching the trace" />}
                </div>
                {sent.trace === null
                  ? <p className="empty-note">
                      {sent.response.request_id === undefined
                        ? "The response carried no request identity, so its trace cannot be followed"
                        : sent.tracePolling ? "Waiting for the edge observation…" : "No trace was recorded for this request"}
                    </p>
                  : <div className="trace-explorer send-trace"><RequestNode request={sent.trace} /></div>}
              </section>
            </div>
          </>
        )}
      </div>
    </>
  );
}

function parseHeaders(text: string): Record<string, string> {
  const headers: Record<string, string> = {};
  for (const line of text.split("\n")) {
    if (line.trim() === "") continue;
    const separator = line.indexOf(":");
    if (separator <= 0) throw new Error(`Header line needs a name and a value: ${line.trim()}`);
    headers[line.slice(0, separator).trim()] = line.slice(separator + 1).trim();
  }
  return headers;
}

function describeFailure(status: number, code: string | undefined): string {
  switch (code) {
    case "app_timeout": return "The App did not answer within 30 seconds";
    case "app_unreachable": return "The App could not be reached";
    case "not_found": return "The live build no longer declares this route";
    default: return status === 400 ? "The request was rejected before sending: check the headers and body" : `The console could not send the request (${status})`;
  }
}

function prettyBody(body: string, contentType: string | undefined): string {
  if (contentType?.includes("json")) {
    try {
      return JSON.stringify(JSON.parse(body), null, 2);
    } catch { /* show as sent */ }
  }
  return body;
}
