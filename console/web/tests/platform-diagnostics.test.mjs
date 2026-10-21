import { describe, expect, mock, test } from "bun:test";

process.env.NODE_ENV = "test";
mock.module("server-only", () => ({}));

describe("Cloudflare platform diagnostics", () => {
  test("returns only safe dispatcher failures", async () => {
    let request;
    const { CloudflarePlatformDiagnostics } = await import("../lib/platform-diagnostics.ts");
    const diagnostics = new CloudflarePlatformDiagnostics({
      accountID: "0123456789abcdef0123456789abcdef",
      apiToken: "observability-token",
      apiBaseURL: "https://cloudflare.example.test/client/v4",
      now: () => Date.parse("2026-08-18T00:00:00Z"),
      fetch: async (url, init) => {
        request = { url, init };
        return Response.json({ success: true, result: { events: { events: [{
          timestamp: Date.parse("2026-08-17T23:59:00Z"),
          $metadata: { message: JSON.stringify({
            source: "cantelop_dispatcher", outcome: "failed", stage: "dispatch",
            status: 502, reason: "api_unavailable", request_id: "req_abc",
            hostname: "vera.cantelop.dev", method: "POST", path: "/slack/events",
            app_id: "app_abc", exception: "must not escape",
          }) },
        }] } } });
      },
    });
    expect(await diagnostics.list()).toEqual({ window_hours: 6, items: [{
      timestamp: "2026-08-17T23:59:00.000Z", outcome: "failed", stage: "dispatch",
      status: 502, reason: "api_unavailable", request_id: "req_abc",
      hostname: "vera.cantelop.dev", method: "POST", path: "/slack/events", app_id: "app_abc",
    }] });
    expect(JSON.parse(request.init.body)).toMatchObject({
      view: "events",
      parameters: { filters: [
        { key: "$metadata.service", value: "cantelop-dispatcher" },
        { key: "$metadata.message", value: '"source":"cantelop_dispatcher"' },
      ] },
    });
  });
});
