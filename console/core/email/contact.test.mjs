import { expect, test } from "bun:test";
import { contactEmail, contactMessage, contactRateLimiter, sendContactEmail } from "./contact";

const environment = { CANTELOP_CONTACT_RESEND_API_KEY: "re_test", CANTELOP_CONTACT_FROM: "Cantelop <contact@notify.cantelop.com>", CANTELOP_CONTACT_TO: "team@cantelop.com" };

test("contact messages are trimmed, bounded, and validated", () => {
  expect(contactMessage({ name: " Ada\r\nLovelace ", email: " ada@example.com ", message: " Hello " }))
    .toEqual({ name: "Ada Lovelace", email: "ada@example.com", message: "Hello" });
  expect(contactMessage({ email: "ada@example.com", message: "Hi" })).toEqual({ name: "", email: "ada@example.com", message: "Hi" });
  for (const input of [
    {}, { email: "ada@example.com" }, { email: "not-an-email", message: "Hi" }, { email: "ada@example.com", message: "   " },
    { email: "ada@example.com", message: "x".repeat(5001) }, { name: "x".repeat(101), email: "ada@example.com", message: "Hi" },
    { name: 1, email: "ada@example.com", message: "Hi" }, { email: ["ada@example.com"], message: "Hi" },
  ]) expect(() => contactMessage(input)).toThrow(TypeError);
});

test("a filled honeypot is dropped without validation", () => {
  expect(contactMessage({ website: "https://spam.example", email: "x", message: "" })).toBeNull();
});

test("contact email escapes visitor input in HTML", () => {
  const email = contactEmail({ name: "<b>Eve</b>", email: "eve@example.com", message: "<script>alert(1)</script>" });
  expect(email.subject).toBe("Contact form: <b>Eve</b>");
  expect(email.html).not.toContain("<script>");
  expect(email.html).toContain("&lt;script&gt;");
  expect(email.text).toContain("<script>alert(1)</script>");
});

test("contact email goes to the team with the visitor as reply-to and a stable idempotency key", async () => {
  const requests = [];
  const fetcher = async (url, init) => { requests.push({ url, init }); return Response.json({ id: "email_1" }); };
  const contact = { name: "Ada", email: "ada@example.com", message: "Hello" };
  expect(await sendContactEmail(contact, environment, fetcher)).toBe("email_1");
  await sendContactEmail(contact, environment, fetcher);
  const body = JSON.parse(requests[0].init.body);
  expect(requests[0].url).toBe("https://api.resend.com/emails");
  expect(requests[0].init.headers.Authorization).toBe("Bearer re_test");
  expect(body).toMatchObject({ from: environment.CANTELOP_CONTACT_FROM, to: ["team@cantelop.com"], reply_to: "ada@example.com" });
  expect(requests[0].init.headers["Idempotency-Key"]).toBe(requests[1].init.headers["Idempotency-Key"]);
});

test("contact email fails closed on missing configuration or provider errors", async () => {
  const contact = { name: "", email: "ada@example.com", message: "Hello" };
  const ok = async () => Response.json({ id: "email_1" });
  await expect(sendContactEmail(contact, { ...environment, CANTELOP_CONTACT_TO: "" }, ok)).rejects.toThrow("not configured");
  await expect(sendContactEmail(contact, { ...environment, CANTELOP_CONTACT_FROM: undefined }, ok)).rejects.toThrow("not configured");
  await expect(sendContactEmail(contact, { CANTELOP_CONTACT_FROM: "a@b.co", CANTELOP_CONTACT_TO: "c@d.co" }, ok)).rejects.toThrow("not configured");
  expect(await sendContactEmail(contact, { CANTELOP_CONTACT_FROM: "a@b.co", CANTELOP_CONTACT_TO: "c@d.co", RESEND_API_KEY: "re_fallback" }, ok)).toBe("email_1");
  await expect(sendContactEmail(contact, environment, async () => new Response("", { status: 422 }))).rejects.toThrow("HTTP 422");
  await expect(sendContactEmail(contact, environment, async () => Response.json({}))).rejects.toThrow("invalid receipt");
});

test("the rate limiter allows a budget per client per window", () => {
  let now = 0;
  const allow = contactRateLimiter(2, 1000, () => now);
  expect([allow("a"), allow("a"), allow("a"), allow("b")]).toEqual([true, true, false, true]);
  now = 1001;
  expect(allow("a")).toBe(true);
});
