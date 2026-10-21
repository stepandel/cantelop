import { listAdminWorkspaces } from "@core/db/admin-workspaces";

import { requireAdminSession } from "@/app/admin/section-data";
import { AdminWorkspacesSection } from "@/components/admin-workspaces-section";

export const dynamic = "force-dynamic";
export const metadata = { title: "Workspaces" };

export default async function AdminWorkspacesPage() {
  await requireAdminSession("workspaces");
  let workspaces = null;
  try {
    workspaces = await listAdminWorkspaces();
  } catch (error) {
    console.error("admin_workspaces_unavailable", error);
    // A database outage degrades this section alone, not the operator frame.
  }
  return <AdminWorkspacesSection workspaces={workspaces} />;
}
