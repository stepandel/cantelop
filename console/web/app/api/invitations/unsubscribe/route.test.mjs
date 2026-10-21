import { expect, spyOn, test } from "bun:test";
import { Pool } from "pg";
import { optOutToken, recipientHash } from "@core/email/preferences";
import { closeControlDatabase } from "@core/db/database";
import { POST } from "./route";

test("one-click unsubscribe cancels an oversized unauthenticated stream", async () => {
  let cancelled = false;
  let pulls = 0;
  const body = new ReadableStream({
    pull(controller) { pulls++; controller.enqueue(new Uint8Array(257)); },
    cancel() { cancelled = true; },
  }, { highWaterMark: 0 });
  const result = await POST(new Request("https://cantelop.com/api/invitations/unsubscribe?token=dummy", {
    method: "POST", body, duplex: "half",
  }));
  expect(result.status).toBe(400);
  expect(cancelled).toBe(true);
  expect(pulls).toBe(1);
});

test("small one-click bodies still require an authentic capability", async () => {
  const result = await POST(new Request("https://cantelop.com/api/invitations/unsubscribe?token=dummy", {
    method: "POST", body: "List-Unsubscribe=One-Click",
  }));
  expect(result.status).toBe(400);
  expect(await result.json()).toEqual({ error: "invalid_preference_link" });
});

test("authentic one-click and browser unsubscribe requests still persist suppression", async () => {
  const previousSecret = process.env.CANTELOP_INVITATION_OPTOUT_SECRET;
  const previousURL = process.env.CANTELOP_DATABASE_URL;
  process.env.CANTELOP_INVITATION_OPTOUT_SECRET = 'test-only-optout-secret-32-characters';
  process.env.CANTELOP_DATABASE_URL = 'postgres://test@localhost/test';
  const query = spyOn(Pool.prototype, 'query').mockResolvedValue({ rows: [], rowCount: 1 });
  try {
    const token = optOutToken('recipient@example.com');
    for (const oneClick of [true, false]) {
      const response = await POST(new Request(`https://cantelop.com/api/invitations/unsubscribe${oneClick ? `?token=${token}` : ''}`, {
        method: 'POST',
        headers: oneClick ? {} : { origin: 'https://cantelop.com', host: 'cantelop.com', 'content-type': 'application/json' },
        body: oneClick ? 'List-Unsubscribe=One-Click' : JSON.stringify({ token }),
      }));
      expect(response.status).toBe(200);
      expect(await response.json()).toEqual({ unsubscribed: true });
    }
    expect(query).toHaveBeenCalledTimes(2);
    for (const [sql, parameters] of query.mock.calls) {
      expect(sql).toContain('INSERT INTO cantelop_control.invitation_suppressions');
      expect(parameters[0]).toBe(recipientHash('recipient@example.com'));
      expect(parameters[1]).toBe('opt_out');
    }
  } finally {
    query.mockRestore();
    await closeControlDatabase();
    if (previousSecret === undefined) delete process.env.CANTELOP_INVITATION_OPTOUT_SECRET;
    else process.env.CANTELOP_INVITATION_OPTOUT_SECRET = previousSecret;
    if (previousURL === undefined) delete process.env.CANTELOP_DATABASE_URL;
    else process.env.CANTELOP_DATABASE_URL = previousURL;
  }
});
