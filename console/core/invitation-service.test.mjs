import { expect, test } from "bun:test";
import { InvitationService } from "./invitation-service";
import { invitationEmail, sendInvitationEmail } from "./email/invitations";

const prepared = {
  invitation: { id: "test-invitation", email: "guest@example.com", delivery: "sending", createdAt: new Date(), expiresAt: new Date() },
  token: "a".repeat(64), appSlug: "sample-app", inviterEmail: "owner@example.com",
};
const environment = { RESEND_API_KEY: "test-key", CANTELOP_INVITATION_FROM: "Cantelop <invites@example.com>", CANTELOP_INVITATION_ORIGIN: "https://cantelop.com", CANTELOP_INVITATION_OPTOUT_SECRET: "s".repeat(32), CANTELOP_INVITATION_REPLY_TO: "stepan@cantelop.com" };

function fixture(sender) {
  const deliveries = [];
  const repository = {
    add: async () => prepared, resend: async () => prepared,
    isSuppressed: async () => false,
    recordDelivery: async (...values) => { deliveries.push(values); return { ...prepared.invitation, delivery: values[1] }; },
  };
  return { repository, deliveries, service: new InvitationService(repository, sender) };
}

test("successful delivery returns safe invitation metadata without its token", async () => {
  const { service, deliveries } = fixture(async (value) => { expect(value).toBe(prepared); return "provider-id"; });
  const response = await service.add("owner", "app", "guest@example.com");
  expect(response).toEqual({ invitation: { ...prepared.invitation, delivery: "sent" } });
  expect(JSON.stringify(response)).not.toContain(prepared.token);
  expect(deliveries).toEqual([[prepared.invitation.id, "sent", "provider-id"]]);
});

test("delivery failures remain visible and an explicit resend can recover", async () => {
  let fail = true;
  const { service, deliveries } = fixture(async () => { if (fail) throw new Error("provider unavailable"); return "provider-id"; });
  expect((await service.add("owner", "app", "guest@example.com")).invitation.delivery).toBe("failed");
  fail = false;
  expect((await service.resend("owner", "app", "invitation")).invitation.delivery).toBe("sent");
  expect(deliveries.map((item) => item[1])).toEqual(["failed", "sent"]);
});

test("existing accounts do not require email configuration or call the sender", async () => {
  const { repository, service, deliveries } = fixture(async () => { throw new Error("must not send"); });
  repository.add = async () => ({ items: [{ userID: "existing" }] });
  expect(await service.add("owner", "app", "guest@example.com")).toEqual({ items: [{ userID: "existing" }] });
  expect(deliveries).toEqual([]);
});

test("Resend receives the intended recipient, idempotency key, timeout, and fragment link", async () => {
  await sendInvitationEmail(prepared, environment, async (url, request) => {
    expect(url).toBe("https://api.resend.com/emails");
    expect(request.headers.Authorization).toBe("Bearer test-key");
    expect(request.headers["Idempotency-Key"]).toBe("app-invitation/test-invitation");
    expect(request.signal).toBeInstanceOf(AbortSignal);
    expect(request.redirect).toBe("error");
    const message = JSON.parse(request.body);
    expect(message.from).toBe(environment.CANTELOP_INVITATION_FROM);
    expect(message.to).toEqual(["guest@example.com"]);
    expect(message.text).toContain(`https://cantelop.com/invitations/accept#token=${prepared.token}`);
    return Response.json({ id: "provider-id" });
  });
});

test("email delivery rejects missing configuration, HTTP errors, and malformed receipts", async () => {
  await expect(sendInvitationEmail(prepared, {})).rejects.toThrow("not configured");
  for (const response of [new Response("denied", { status: 403 }), Response.json({}), Response.json({ id: "" })]) {
    await expect(sendInvitationEmail(prepared, environment, async () => response)).rejects.toThrow();
  }
  await expect(sendInvitationEmail(prepared, environment, async () => { throw new Error("timeout"); })).rejects.toThrow("timeout");
});

test("email escapes untrusted text and validates configured origins", () => {
  const message = invitationEmail({ ...prepared, inviterEmail: '<img src=x>@example.com' }, "https://cantelop.com");
  expect(message.html).not.toContain("<img src=x>");
  expect(message.html).toContain("&lt;img");
  for (const origin of ["http://example.com", "https://user:password@example.com", "https://example.com/path", "https://example.com?query=1"]) {
    expect(() => invitationEmail(prepared, origin)).toThrow();
  }
  expect(invitationEmail(prepared, "http://localhost:3000").text).toContain("http://localhost:3000/invitations/accept");
});
