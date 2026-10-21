import { requireAppSession } from "@/app/apps/[id]/section-data";
import { AppAccessPanel } from "@/components/app-access-panel";
import { SectionHead } from "@/components/section-head";
import { invitationControl } from "@core/invitation-control";
import { productControl } from "@core/product-control";

export const dynamic = "force-dynamic";
export const metadata = { title: "Access" };

export default async function AccessPage({ params }: { params: Promise<{ id: string }> }) {
  const { id } = await params;
  const session = await requireAppSession(id, "access");
  const access = await productControl().getAppAccess(session.user.id, id);
  const canManage = access.owner.userID === session.user.id;
  const invitations = canManage ? await invitationControl().list(session.user.id, id) : [];
  return (
    <>
      <SectionHead title="Access" />
      <div className="section-panel">
        <AppAccessPanel appId={id} owner={access.owner} initialCollaborators={access.collaborators} initialInvitations={invitations} canManage={canManage} />
      </div>
    </>
  );
}
