import { describe, expect, test } from "bun:test";

import { publishObservations, type ObservationIngestEnv } from "../src";

const environment: ObservationIngestEnv = {
  OBSERVABILITY_INGEST_ORIGIN: "https://console.cantelop.dev",
  OBSERVABILITY_INGEST_TOKEN: "a".repeat(64),
};

const event = {
  event_id: "log_0123456789abcdef0123456789abcdef",
  schema_version: 1,
  source: "edge",
  type: "log.recorded",
  occurred_at: "2026-09-01T12:00:00.000Z",
  app_id: "app_0123456789abcdef0123456789abcdef",
  request_id: "req_abcdefabcdefabcdefabcdefabcdefab",
  trace_id: "abcdefabcdefabcdefabcdefabcdefab",
  log_id: "log_0123456789abcdef0123456789abcdef",
  severity: "info",
  body: "handler entered",
  attributes: { surface: "edge" },
};

describe("edge observation publisher", () => {
  test("authenticates and sends one bounded observation batch", async () => {
    let sent: { input: RequestInfo | URL; init?: RequestInit } | undefined;
    await publishObservations(environment, [event], {
      userAgent: "cantelop-test/1",
      request: async (input, init) => {
        sent = { input, init };
        return new Response(null, { status: 202 });
      },
    });
    expect(String(sent?.input)).toBe("https://console.cantelop.dev/api/internal/observability/events");
    const headers = new Headers(sent?.init?.headers);
    expect(headers.get("Authorization")).toBe(`Bearer ${"a".repeat(64)}`);
    expect(headers.get("User-Agent")).toBe("cantelop-test/1");
    expect(String(sent?.init?.body)).toBe(JSON.stringify({ events: [event] }));
  });

  test("uses the configured ingestion origin", async () => {
    let target = "";
    await publishObservations({ ...environment, OBSERVABILITY_INGEST_ORIGIN: "https://console.example.test/" }, [event], {
      userAgent: "cantelop-test/1",
      request: async (input) => {
        target = String(input);
        return new Response(null, { status: 202 });
      },
    });
    expect(target).toBe("https://console.example.test/api/internal/observability/events");
  });

  test("rejects invalid configuration and unsuccessful ingestion", async () => {
    const options = { userAgent: "cantelop-test/1", request: async () => new Response(null, { status: 202 }) };
    expect(publishObservations({ ...environment, OBSERVABILITY_INGEST_TOKEN: "invalid" }, [event], options))
      .rejects.toThrow("invalid observability ingestion credential");
    for (const origin of ["", "http://console.cantelop.dev", "https://console.cantelop.dev/api", "https://user@console.cantelop.dev", "https://console.cantelop.dev?x"]) {
      expect(publishObservations({ ...environment, OBSERVABILITY_INGEST_ORIGIN: origin }, [event], options))
        .rejects.toThrow("invalid observability ingestion origin");
    }
    expect(publishObservations(environment, [event], {
      userAgent: "cantelop-test/1",
      request: async () => new Response(null, { status: 503 }),
    })).rejects.toThrow("HTTP 503");
  });

  test("does not contact ingestion for empty lists", async () => {
    let calls = 0;
    await publishObservations({ ...environment, OBSERVABILITY_INGEST_TOKEN: "invalid" }, [], {
      userAgent: "cantelop-test/1",
      request: async () => {
        calls += 1;
        return new Response();
      },
    });
    expect(calls).toBe(0);
  });

  test("splits large batches before the ingestion limits without reordering", async () => {
    const observations = Array.from({ length: 300 }, (_, index) => ({
      ...event,
      event_id: `log_${index.toString(16).padStart(32, "0")}`,
      body: "x".repeat(index < 256 ? 16_384 : 1),
      attributes: { source: index === 299 ? "invocation" : "console.log" },
    }));
    const sent: unknown[] = [];
    let calls = 0;
    await publishObservations(environment, observations, {
      userAgent: "cantelop-test/1",
      request: async (_input, init) => {
        const body = String(init?.body);
        expect(new TextEncoder().encode(body).length).toBeLessThanOrEqual(1 << 20);
        const batch = JSON.parse(body).events as unknown[];
        expect(batch.length).toBeLessThanOrEqual(256);
        sent.push(...batch);
        calls++;
        return new Response(null, { status: 202 });
      },
    });
    expect(calls).toBeGreaterThan(1);
    expect(sent).toEqual(observations);
  });
});
