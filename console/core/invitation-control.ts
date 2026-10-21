import "server-only";
import { controlDatabase } from "./db/database";
import { InvitationRepository } from "./db/invitation-repository";
import { sendInvitationEmail } from "./email/invitations";
import { InvitationService } from "./invitation-service";

let shared: InvitationService | undefined;
export function invitationControl(): InvitationService {
  return shared ??= new InvitationService(new InvitationRepository(controlDatabase()), sendInvitationEmail);
}
