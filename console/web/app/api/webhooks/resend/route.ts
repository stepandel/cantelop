import { controlDatabase } from "@core/db/database";
import { InvitationRepository } from "@core/db/invitation-repository";
import { receiveEmailEvent } from "@core/email/webhooks";
export const runtime = "nodejs";
export const dynamic = "force-dynamic";
export function POST(request: Request) {
  return receiveEmailEvent(request, new InvitationRepository(controlDatabase()));
}
