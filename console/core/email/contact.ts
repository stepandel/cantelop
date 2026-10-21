import { createHash } from "node:crypto";

export type ContactMessage = { name: string; email: string; message: string };

const emailPattern = /^[^\s@<>]+@[^\s@<>]+\.[^\s@<>]+$/;

function escapeHTML(text: string) {
  return text.replace(/[&<>"']/g, (character) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[character]!);
}

/** Returns null for a filled honeypot so the caller can answer as if it sent. */
export function contactMessage(input: Record<string, unknown>): ContactMessage | null {
  if (typeof input.website === "string" && input.website) return null;
  const { name = "", email, message } = input;
  if (typeof name !== "string" || typeof email !== "string" || typeof message !== "string") throw new TypeError("invalid contact message");
  // Collapse whitespace in the name: it lands in a single-line subject.
  const parsed = { name: name.replace(/\s+/g, " ").trim(), email: email.trim(), message: message.trim() };
  if (parsed.name.length > 100 || parsed.email.length > 254 || !emailPattern.test(parsed.email) ||
      !parsed.message || parsed.message.length > 5000) throw new TypeError("invalid contact message");
  return parsed;
}

export function contactEmail({ name, email, message }: ContactMessage) {
  const sender = name ? `${name} <${email}>` : email;
  return {
    subject: `Contact form: ${name || email}`,
    text: `From: ${sender}\n\n${message}\n\nReply to this email to answer them.`,
    html: `<!doctype html><html lang="en"><head><meta charset="utf-8"></head>
<body style="margin:0;padding:24px;font-family:Arial,Helvetica,sans-serif;color:#0B2E22">
<p style="margin:0 0 16px;font-size:14px;color:#476052">From ${escapeHTML(sender)}</p>
<p style="margin:0 0 24px;font-size:15px;line-height:24px;white-space:pre-wrap">${escapeHTML(message)}</p>
<p style="margin:0;font-size:12px;color:#526257">Reply to this email to answer them.</p>
</body></html>`,
  };
}

export async function sendContactEmail(
  contact: ContactMessage,
  environment: NodeJS.ProcessEnv = process.env,
  fetcher: typeof fetch = fetch,
): Promise<string> {
  const key = environment.CANTELOP_CONTACT_RESEND_API_KEY?.trim() ||
    environment.CANTELOP_INVITATION_RESEND_API_KEY?.trim() || environment.RESEND_API_KEY?.trim();
  const from = environment.CANTELOP_CONTACT_FROM?.trim();
  const to = environment.CANTELOP_CONTACT_TO?.trim();
  if (!key || !from || !to || !emailPattern.test(to)) throw new Error("Contact email is not configured");
  // A double-clicked or retried submission sends once; Resend keeps keys for 24 hours.
  const digest = createHash("sha256").update(JSON.stringify([contact.email, contact.message])).digest("hex");
  const response = await fetcher("https://api.resend.com/emails", {
    method: "POST",
    headers: { Authorization: `Bearer ${key}`, "Content-Type": "application/json", "Idempotency-Key": `contact/${digest}` },
    body: JSON.stringify({ from, to: [to], reply_to: contact.email, ...contactEmail(contact) }),
    signal: AbortSignal.timeout(10_000),
    redirect: "error",
  });
  if (!response.ok) throw new Error(`Contact email provider rejected the request (HTTP ${response.status})`);
  const result = await response.json() as { id?: unknown };
  if (typeof result.id !== "string" || !result.id) throw new Error("Contact email provider returned an invalid receipt");
  return result.id;
}

/**
 * A per-instance budget per client address. Serverless instances do not share
 * it, so it slows a single noisy client rather than enforcing a global limit.
 */
export function contactRateLimiter(limit = 5, windowMs = 60 * 60 * 1000, now = () => Date.now()) {
  const hits = new Map<string, number[]>();
  return (client: string): boolean => {
    const cutoff = now() - windowMs;
    if (hits.size > 10_000) hits.clear();
    const recent = (hits.get(client) ?? []).filter((at) => at > cutoff);
    if (recent.length >= limit) { hits.set(client, recent); return false; }
    recent.push(now());
    hits.set(client, recent);
    return true;
  };
}
