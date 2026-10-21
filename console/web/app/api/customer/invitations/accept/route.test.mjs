import { expect, mock, test } from "bun:test";

let userSession;
let acceptedEmail;
mock.module("@/lib/verified-user-session", () => ({ getVerifiedUserSession: async () => userSession }));
mock.module("@core/invitation-control", () => ({
  invitationControl: () => ({
    accept: async (_userID, verifiedEmail) => { acceptedEmail = verifiedEmail; return `app_${"a".repeat(32)}`; },
  }),
}));
const { POST } = await import("./route.ts");

test("invitation acceptance uses verified OAuth email rather than stale product email", async () => {
  userSession = { user: {
    id: `usr_${"b".repeat(32)}`, email: "admin@example.com",
    verifiedEmail: "changed@example.com", login: "admin",
  } };
  const request = new Request("https://console.cantelop.dev/api/customer/invitations/accept", {
    method: "POST",
    headers: { host: "console.cantelop.dev", origin: "https://console.cantelop.dev", "content-type": "application/json" },
    body: JSON.stringify({ token: "invitation-token" }),
  });
  expect((await POST(request)).status).toBe(200);
  expect(acceptedEmail).toBe("changed@example.com");
});

test("invitation acceptance asks for a fresh sign-in when the provider cannot confirm the address", async () => {
  acceptedEmail = undefined;
  userSession = { user: {
    id: `usr_${"b".repeat(32)}`, email: "admin@example.com", verifiedEmail: null, login: "admin",
  } };
  const request = new Request("https://console.cantelop.dev/api/customer/invitations/accept", {
    method: "POST",
    headers: { host: "console.cantelop.dev", origin: "https://console.cantelop.dev", "content-type": "application/json" },
    body: JSON.stringify({ token: "invitation-token" }),
  });
  const response = await POST(request);
  expect(response.status).toBe(401);
  expect(await response.json()).toEqual({ error: "reauthentication_required" });
  expect(acceptedEmail).toBeUndefined();
});
