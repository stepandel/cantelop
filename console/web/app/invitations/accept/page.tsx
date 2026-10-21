import { ConsoleShell } from "@/components/console-shell";
import { InvitationAccept } from "@/components/invitation-accept";
import { getVerifiedUserSession } from "@/lib/verified-user-session";
import { viewerOf } from "@/lib/viewer";

export const dynamic = "force-dynamic";
export const metadata = { title: "Accept invitation", referrer: "no-referrer" };

export default async function InvitationPage() {
  const session = await getVerifiedUserSession();
  if (!session) return <main className="invitation-landing"><InvitationAccept email={null} /></main>;
  return (
    <ConsoleShell viewer={viewerOf(session)} crumbs={[{ label: "Invitation" }]}>
      {/* Acceptance checks the provider-confirmed address, so that is the one
          to show; without it the person has to sign in again. */}
      <InvitationAccept email={session.user.verifiedEmail ?? session.user.email ?? ""} reauthenticate={!session.user.verifiedEmail} />
    </ConsoleShell>
  );
}
