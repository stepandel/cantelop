import { loadHostSnapshot, requireAdminSession } from "@/app/admin/section-data";
import { DashboardView } from "@/components/dashboard-view";

export const dynamic = "force-dynamic";
export const metadata = { title: "Sandboxes" };

export default async function AdminSandboxesPage() {
  await requireAdminSession("sandboxes");
  const { snapshot, unavailable } = await loadHostSnapshot();
  return <DashboardView snapshot={snapshot} unavailable={unavailable} view="sandboxes" canStopSandboxes />;
}
