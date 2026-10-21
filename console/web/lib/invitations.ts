import type { AppInvitation } from "@core/db/invitation-repository";

/** The core row as JSON delivers it: the dates arrive as strings. */
export type Invitation = Omit<AppInvitation, "createdAt" | "expiresAt"> & { createdAt: Date | string; expiresAt: Date | string };

export function invitationStatus(invitation: Invitation, now: number | null): string {
  if (invitation.delivery === "suppressed") return "Email stopped";
  if (invitation.delivery === "bounced") return "Bounced";
  if (invitation.delivery === "complained") return "Marked as spam";
  if (now !== null && new Date(invitation.expiresAt).getTime() <= now) return "Expired";
  if (invitation.delivery === "failed") return "Send failed";
  if (invitation.delivery === "sending") {
    return now !== null && now - new Date(invitation.createdAt).getTime() > 60_000 ? "Retry send" : "Sending…";
  }
  if (invitation.delivery === "delivered") return "Delivered to mail server";
  if (invitation.delivery === "delayed") return "Delivery delayed";
  return "Submitted";
}

export const invitationStorageKey = "cantelop.pending-invitation";
export const invitationAcceptancePath = "/invitations/accept";

export function rememberInvitation(fragment: string, storage: Pick<Storage, "getItem" | "setItem" | "removeItem">): string | null {
  const parameters = new URLSearchParams(fragment.replace(/^#/, ""));
  const fresh = parameters.get("token");
  if (fresh !== null) {
    if (!/^[0-9a-f]{64}$/.test(fresh)) {
      storage.removeItem(invitationStorageKey);
      return null;
    }
    storage.setItem(invitationStorageKey, fresh);
    return fresh;
  }
  const saved = storage.getItem(invitationStorageKey);
  return saved && /^[0-9a-f]{64}$/.test(saved) ? saved : null;
}
