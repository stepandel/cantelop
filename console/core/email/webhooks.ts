import { Webhook } from "svix";
import { readBoundedUTF8Body } from "../http/bounded-body";
import type { InvitationRepository, AppInvitation } from "../db/invitation-repository";

const statuses: Record<string, AppInvitation["delivery"]> = {
  "email.sent": "sent", "email.delivered": "delivered", "email.delivery_delayed": "delayed",
  "email.failed": "failed", "email.bounced": "bounced", "email.complained": "complained", "email.suppressed": "suppressed",
};

export async function receiveEmailEvent(request: Request, repository: Pick<InvitationRepository, "recordEvent">, environment = process.env): Promise<Response> {
  const secret = environment.RESEND_WEBHOOK_SECRET;
  if (!secret) return new Response("Webhook not configured", { status: 503 });
  if (Number(request.headers.get("content-length")) > 65_536) return new Response("Too large", { status: 413 });
  let body: string;
  try { body = await readBoundedUTF8Body(request.body, 65_536); }
  catch { return new Response("Invalid or oversized body", { status: 413 }); }
  let event: { type: string; created_at: string; data: { email_id: string; to: string[]; tags?: Record<string, string> | { name: string; value: string }[]; bounce?: { type?: string } } };
  const id = request.headers.get("svix-id") ?? "";
  try {
    new Webhook(secret).verify(body, {
      "svix-id": id,
      "svix-timestamp": request.headers.get("svix-timestamp") ?? "",
      "svix-signature": request.headers.get("svix-signature") ?? "",
    });
    event = JSON.parse(body);
    if (!event || typeof event.type !== "string") throw new Error();
  } catch { return new Response("Invalid signature", { status: 400 }); }
  let delivery = statuses[event.type];
  if (!delivery) return new Response(null, { status: 204 });
  const createdAt = new Date(event.created_at);
  if (!event.data || typeof event.data.email_id !== "string" || !Array.isArray(event.data.to) || event.data.to.length !== 1 || typeof event.data.to[0] !== "string" || !Number.isFinite(createdAt.getTime())) {
    return new Response("Invalid event", { status: 400 });
  }
  // Transient bounces should not permanently suppress a recipient.
  if (delivery === "bounced" && event.data.bounce?.type?.toLowerCase() === "transient") delivery = "delayed";
  const tags = event.data.tags;
  const invitationID = Array.isArray(tags) ? tags.find((tag) => tag.name === "invitation_id")?.value : tags?.invitation_id;
  // Database failures propagate as 5xx, allowing Resend to retry.
  await repository.recordEvent({ id, invitationID, providerID: event.data.email_id, email: event.data.to[0], delivery, createdAt });
  return new Response(null, { status: 204 });
}
