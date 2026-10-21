import { describe, expect, test } from "bun:test";

import { EDGE_HEADERS } from "@cantelop/edge-contracts";

import { observationsFromTail, type EdgeTailItem } from "../src/core";

const appID = "app_0123456789abcdef0123456789abcdef";
const requestID = "req_abcdefabcdefabcdefabcdefabcdefab";

function item(overrides: Partial<EdgeTailItem> = {}): EdgeTailItem {
  return {
    scriptName: appID,
    eventTimestamp: Date.parse("2026-09-01T12:00:00.100Z"),
    event: { request: { headers: { [EDGE_HEADERS.requestId]: requestID } } },
    logs: [{
      level: "log",
      message: ["handler entered", { accepted: true }],
      timestamp: Date.parse("2026-09-01T12:00:00.010Z"),
    }],
    exceptions: [{
      name: "Error",
      message: "handler failed",
      timestamp: Date.parse("2026-09-01T12:00:00.020Z"),
    }],
    diagnosticsChannelEvents: [],
    outcome: "exception",
    cpuTime: 4.5,
    wallTime: 100,
    truncated: true,
    ...overrides,
  };
}

describe("edge Tail observations", () => {
  test("converts console output, exceptions, outcomes, and truncation into request logs", async () => {
    const observations = await observationsFromTail([item()]);
    expect(observations.map(({ severity }) => severity)).toEqual(["info", "error", "error", "warn"]);
    expect(observations[0]).toMatchObject({
      app_id: appID,
      request_id: requestID,
      trace_id: requestID.slice(4),
      body: 'handler entered {"accepted":true}',
      attributes: { source: "console.log", producer: "user_worker" },
    });
    expect(observations[1].body).toBe("Error: handler failed");
    expect(observations[2].attributes).toMatchObject({ source: "invocation", outcome: "exception" });
    expect(observations[2].body).toBe("edge Worker invocation completed · outcome exception · CPU 4.5 ms · wall 100 ms");
    expect(observations.every(({ log_id }) => /^log_[0-9a-f]{32}$/.test(log_id))).toBe(true);
  });

  test("uses deterministic identities for Tail delivery retries", async () => {
    const first = await observationsFromTail([item()]);
    const second = await observationsFromTail([item()]);
    expect(second.map(({ log_id }) => log_id)).toEqual(first.map(({ log_id }) => log_id));
  });

  test("ignores unrelated Workers and uncorrelated user invocations", async () => {
    expect(await observationsFromTail([
      item({ scriptName: "cantelop-dispatcher" }),
      item({ event: { request: { headers: {} } } }),
    ])).toEqual([]);
  });

  test("collects tenant-scoped dispatcher diagnostics", async () => {
    const observations = await observationsFromTail([item({
      scriptName: "cantelop-dispatcher",
      event: { request: { headers: {} } },
      logs: [{
        level: "error",
        timestamp: Date.parse("2026-09-01T12:00:00.010Z"),
        message: [JSON.stringify({ app_id: appID, request_id: requestID, reason: "api_unavailable" })],
      }],
      exceptions: [],
      truncated: false,
    })]);
    expect(observations).toHaveLength(1);
    expect(observations[0]).toMatchObject({
      request_id: requestID,
      severity: "error",
      attributes: { producer: "dispatcher" },
    });
  });

  test("normalizes invalid and oversized tenant log fields while retaining invocation", async () => {
    const observations = await observationsFromTail([item({
      logs: [{ level: "x".repeat(20_000), message: ["bad\0body\r"], timestamp: Date.now() }],
      exceptions: [{ name: "N".repeat(20_000), message: "failure", timestamp: Date.now() }],
      diagnosticsChannelEvents: [{ channel: "C".repeat(20_000), message: "diagnostic", timestamp: Date.now() }],
      outcome: "O".repeat(20_000),
      truncated: false,
    })]);
    expect(observations).toHaveLength(4);
    expect(observations.at(-1)?.attributes.source).toBe("invocation");
    for (const observation of observations) {
      expect(observation.body.length).toBeLessThanOrEqual(16_384);
      expect(observation.body).not.toMatch(/[\0\r]/);
      expect(new TextEncoder().encode(JSON.stringify(observation.attributes)).length).toBeLessThanOrEqual(16_384);
    }
  });

  test("retains invocation when a platform log message is scalar", async () => {
    const observations = await observationsFromTail([item({
      logs: [{ level: "log", message: "scalar log", timestamp: Date.now() }],
      exceptions: [], diagnosticsChannelEvents: [], truncated: false,
    })]);
    expect(observations.map(({ body }) => body)).toEqual([
      "scalar log", "edge Worker invocation completed · outcome exception · CPU 4.5 ms · wall 100 ms",
    ]);
  });
});
