import { expect, test } from "bun:test";
import { Webhook } from "svix";
import { optOutToken, recipientHash, verifyOptOutToken } from "./preferences";
import { receiveEmailEvent } from "./webhooks";
import { InvitationService } from "../invitation-service";
const environment = { CANTELOP_INVITATION_OPTOUT_SECRET: "secret".repeat(8) };
test("preference links are normalized, tamper-resistant and cannot grant app access", () => {
  const token = optOutToken(" Guest@Example.com ", environment);
  expect(token).toBe(optOutToken("guest@example.com", environment));
  expect(token).not.toContain("guest");
  expect(verifyOptOutToken(token, environment)).toBe(recipientHash("guest@example.com"));
  expect(() => verifyOptOutToken(token.replace(/^v1./, "v2."), environment)).toThrow();
  expect(() => verifyOptOutToken(token.slice(0, -1) + (token.endsWith("0") ? "1" : "0"), environment)).toThrow();
  expect(() => verifyOptOutToken("a".repeat(64), environment)).toThrow();
  expect(() => optOutToken("guest@example.com", {})).toThrow();
});
const webhookSecret = "whsec_" + Buffer.from("webhook-test-secret-32-bytes-long!").toString("base64");
function request(event, at = new Date(), bodyOverride) {
  const payload = JSON.stringify(event);
  const id = "msg_test";
  return new Request("https://cantelop.com/api/webhooks/resend", { method: "POST", body: bodyOverride ?? payload,
    headers: { "svix-id": id, "svix-timestamp": String(Math.floor(at.getTime() / 1000)), "svix-signature": new Webhook(webhookSecret).sign(id, at, payload) } });
}
const event = { type: "email.delivered", created_at: new Date().toISOString(), data: { email_id: "provider-id", to: ["guest@example.com"], tags: { invitation_id: "invitation-id" } } };
test("webhooks cancel oversized streams before storage, regardless of declared length", async () => {
  for (const length of [undefined, "invalid", "1"]) {
    let cancelled = false;
    let pulls = 0;
    const body = new ReadableStream({
      pull(controller) { pulls++; controller.enqueue(new Uint8Array(32769)); },
      cancel() { cancelled = true; },
    }, { highWaterMark: 0 });
    const req = new Request("https://cantelop.com/api/webhooks/resend", {
      method: "POST", body, duplex: "half", headers: length === undefined ? {} : { "content-length": length },
    });
    const result = await receiveEmailEvent(req, { recordEvent() { throw new Error("unexpected write"); } }, { RESEND_WEBHOOK_SECRET: webhookSecret });
    expect(result.status).toBe(413);
    expect(cancelled).toBe(true);
    expect(pulls).toBe(2);
  }
});
test("webhooks verify raw bytes and timestamps before touching storage", async () => {
  const events = []; const repository = { recordEvent: async (event) => events.push(event) };
  const env = { RESEND_WEBHOOK_SECRET: webhookSecret };
  expect((await receiveEmailEvent(request(event), repository, env)).status).toBe(204);
  expect(events[0]).toMatchObject({ providerID: "provider-id", invitationID: "invitation-id", delivery: "delivered" });
  expect((await receiveEmailEvent(request(event, new Date(Date.now() - 600_000)), repository, env)).status).toBe(400);
  expect((await receiveEmailEvent(request(event, new Date(), JSON.stringify({ ...event, type: "email.complained" })), repository, env)).status).toBe(400);
  expect(events).toHaveLength(1);
  expect((await receiveEmailEvent(request(event), repository, {})).status).toBe(503);
});
test("transient bounce does not suppress and database failures are retried", async () => {
  const env = { RESEND_WEBHOOK_SECRET: webhookSecret }; const events = [];
  const bounce = { ...event, type: "email.bounced", data: { ...event.data, bounce: { type: "Transient" } } };
  await receiveEmailEvent(request(bounce), { recordEvent: async (event) => events.push(event) }, env);
  expect(events[0].delivery).toBe("delayed");
  await expect(receiveEmailEvent(request(event), { recordEvent: async () => { throw new Error("database unavailable"); } }, env)).rejects.toThrow("database unavailable");
});
test("suppression is checked again immediately before sending", async () => {
  let sent = false;
  const prepared = { invitation: { id: "invitation", email: "guest@example.com" } };
  const repository = { add: async () => prepared, isSuppressed: async () => true, recordDelivery: async (id, delivery) => ({ id, delivery }) };
  const service = new InvitationService(repository, async () => { sent = true; return "id"; });
  expect((await service.add("owner", "app", "guest@example.com")).invitation.delivery).toBe("suppressed");
  expect(sent).toBe(false);
});
