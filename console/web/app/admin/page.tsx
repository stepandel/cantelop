import { loadAdminCounts, loadHostSnapshot, requireAdminSession } from "@/app/admin/section-data";
import { DashboardView } from "@/components/dashboard-view";

export const dynamic = "force-dynamic";

export const metadata = { title: "Host telemetry" };

// The operator's home: the host header over the infrastructure graphs — the
// first thing to look at when opening the console.
export default async function AdminHomePage() {
  await requireAdminSession();
  const [{ snapshot, unavailable }, counts] = await Promise.all([loadHostSnapshot(), loadAdminCounts()]);
  return <DashboardView snapshot={snapshot} unavailable={unavailable} counts={counts} view="home" />;
}
