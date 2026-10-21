import { expect, test } from "bun:test";
import { invitationErrorResponse, invitationInput, validInvitationMutation } from "../lib/invitation-http";
import { InvitationError } from "../../core/db/invitation-repository";

function request(body = '{}', origin = 'https://cantelop.com', type = 'application/json') {
  return new Request('https://cantelop.com/api/customer/invitations/accept', {
    method: 'POST', body, headers: { origin, host: 'cantelop.com', 'content-type': type },
  });
}

test("invitation mutations reject foreign origins and non-JSON requests", () => {
  expect(validInvitationMutation(request())).toBe(true);
  expect(validInvitationMutation(request('{}', 'https://attacker.example'))).toBe(false);
  expect(validInvitationMutation(request('{}', 'https://cantelop.com', 'text/plain'))).toBe(false);
});

test("invitation input rejects malformed, oversized, and non-object JSON", async () => {
  for (const body of ['', '{', 'null', '[]', 'true', '"value"', ' '.repeat(1025)]) {
    await expect(invitationInput(request(body))).rejects.toBeInstanceOf(TypeError);
  }
  expect(await invitationInput(request('{"token":"test"}'))).toEqual({ token: "test" });
});

test("invitation input bounds streaming bytes and preserves the Unicode character budget", async () => {
  let cancelled = false;
  const body = new ReadableStream({
    pull(controller) { controller.enqueue(new Uint8Array(4097)); },
    cancel() { cancelled = true; },
  }, { highWaterMark: 0 });
  await expect(invitationInput(new Request('https://cantelop.com/api/invitations/unsubscribe', {
    method: 'POST', body, duplex: 'half',
  }))).rejects.toBeInstanceOf(TypeError);
  expect(cancelled).toBe(true);
  const value = { token: '界'.repeat(1000) };
  expect(await invitationInput(request(JSON.stringify(value)))).toEqual(value);
});

test("invitation errors return actionable status codes and are never cacheable", async () => {
  for (const [code, status] of Object.entries({ invalid_invitation: 410, email_mismatch: 403, already_invited: 409, invite_rate_limited: 429 })) {
    const response = invitationErrorResponse(new InvitationError(code));
    expect(response.status).toBe(status);
    expect(response.headers.get('cache-control')).toBe('private, no-store');
    expect(await response.json()).toEqual({ error: code });
  }
});
