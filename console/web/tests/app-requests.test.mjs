import { describe, expect, mock, test } from "bun:test";

process.env.NODE_ENV = "test";
mock.module("server-only", () => ({}));

const appID = "app_0123456789abcdef0123456789abcdef";

describe("Cloudflare App requests", () => {
  test("groups safe invocation metadata and application logs", async () => {
    let request;
    const { CloudflareAppRequests } = await import("../lib/app-requests.ts");
    const requests = new CloudflareAppRequests({
      accountID: "0123456789abcdef0123456789abcdef", apiToken: "observability-token",
      apiBaseURL: "https://cloudflare.example.test/client/v4", now: () => Date.parse("2026-08-18T00:00:00Z"),
      fetch: async (url, init) => {
        request = { url, init };
        return Response.json({ success: true, result: { invocations: {
          "cf-request-1": [
            { timestamp: Date.parse("2026-08-17T23:59:00Z"), source: {}, $metadata: {
              type: "cf-worker-event", statusCode: 202, duration: 12.5, outcome: "ok",
              trigger: "POST /slack/events?token=secret", url: "https://vera.cantelop.dev/slack/events?token=secret",
            } },
            { timestamp: Date.parse("2026-08-17T23:59:00.010Z"), source: {}, $metadata: {
              type: "cf-worker-log", level: "info", message: '{"source":"slack_events","outcome":"dispatched"}',
            } },
          ],
        } } });
      },
    });
    expect(await requests.list(appID)).toEqual({ window_hours: 6, items: [{
      request_id: "cf-request-1", timestamp: "2026-08-17T23:59:00.000Z", method: "POST", path: "/slack/events",
      status: 202, duration_ms: 12.5, outcome: "ok",
      logs: [{ timestamp: "2026-08-17T23:59:00.010Z", level: "info", message: '{"source":"slack_events","outcome":"dispatched"}' }],
    }] });
    expect(JSON.parse(request.init.body)).toMatchObject({ view: "invocations", parameters: { filters: [{ value: appID }] } });
  });

  test("cancels an oversized streamed telemetry response", async () => {
    const { CloudflareAppRequests } = await import("../lib/app-requests.ts");
    let canceled = false;
    const body = new ReadableStream({
      start(controller) {
        controller.enqueue(new Uint8Array(2 << 20));
        controller.enqueue(new Uint8Array([1]));
      },
      cancel() { canceled = true; },
    });
    const requests = new CloudflareAppRequests({
      accountID: "0123456789abcdef0123456789abcdef", apiToken: "observability-token",
      fetch: async () => new Response(body),
    });
    await expect(requests.list(appID)).rejects.toThrow("Cloudflare request response is too large");
    expect(canceled).toBe(true);
  });
});
