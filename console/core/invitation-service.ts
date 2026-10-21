import type { AppInvitation, InvitationRepository, PreparedInvitation } from "./db/invitation-repository";

export type InvitationSender = (prepared: PreparedInvitation) => Promise<string>;

export class InvitationService {
  constructor(private readonly repository: InvitationRepository, private readonly send: InvitationSender) {}

  async add(ownerID: string, appID: string, email: string) {
    const result = await this.repository.add(ownerID, appID, email);
    return "items" in result ? result : { invitation: await this.deliver(result) };
  }

  async resend(ownerID: string, appID: string, invitationID: string) {
    return { invitation: await this.deliver(await this.repository.resend(ownerID, appID, invitationID)) };
  }

  list(ownerID: string, appID: string) { return this.repository.list(ownerID, appID); }
  revoke(ownerID: string, appID: string, invitationID: string) { return this.repository.revoke(ownerID, appID, invitationID); }
  accept(userID: string, verifiedEmail: string, token: string) { return this.repository.accept(userID, verifiedEmail, token); }

  private async deliver(prepared: PreparedInvitation): Promise<AppInvitation> {
    if (await this.repository.isSuppressed(prepared.invitation.email)) {
      return this.repository.recordDelivery(prepared.invitation.id, "suppressed");
    }
    let providerID: string | undefined;
    let delivery: "sent" | "failed" = "sent";
    try {
      providerID = await this.send(prepared);
    } catch (error) {
      // Name and message only: the sender never puts the recipient, token, or
      // provider body in either, and operators need to see a bad key, sender,
      // or origin. The pending row remains available for an explicit retry.
      console.error("invitation email failed", prepared.invitation.id,
        error instanceof Error ? `${error.name}: ${error.message}` : String(error));
      delivery = "failed";
    }
    return this.repository.recordDelivery(prepared.invitation.id, delivery, providerID);
  }
}
