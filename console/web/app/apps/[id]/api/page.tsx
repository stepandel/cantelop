import { notFound } from "next/navigation";

import { requireAppSession } from "@/app/apps/[id]/section-data";
import { ApiRoutesSection } from "@/components/api-routes-section";
import { appRoutes } from "@core/api-routes/control";
import type { AppRouteOverview } from "@core/api-routes/overview";
import { productControl } from "@core/product-control";

export const dynamic = "force-dynamic";
export const metadata = { title: "API" };

export default async function ApiPage({ params }: { params: Promise<{ id: string }> }) {
  const { id } = await params;
  const session = await requireAppSession(id, "api");
  let hostname: string;
  try {
    hostname = (await productControl().getApp(session.user.id, id)).hostname;
  } catch {
    notFound();
  }
  let initial: AppRouteOverview | null = null;
  try {
    initial = await appRoutes(session.user.id, id);
  } catch (error) {
    console.error("app_routes_unavailable", error);
    // The client keeps the section available and retries.
  }
  return <ApiRoutesSection appId={id} appHostname={hostname} initial={initial} tracesHrefBase={`/apps/${id}/traces?path=`} routeHrefBase={`/apps/${id}/api/route?`} />;
}
