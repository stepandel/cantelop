import { notFound } from "next/navigation";

import { requireAppSession } from "@/app/apps/[id]/section-data";
import { ApiRouteDetail } from "@/components/api-route-detail";
import { appRouteDetail } from "@core/api-routes/control";
import type { AppRouteDetail } from "@core/api-routes/detail";
import { routeQuery } from "@core/api-routes/query";
import { productControl } from "@core/product-control";

export const dynamic = "force-dynamic";
export const metadata = { title: "Route" };

export default async function ApiRoutePage({
  params,
  searchParams,
}: {
  params: Promise<{ id: string }>;
  searchParams: Promise<Record<string, string | string[] | undefined>>;
}) {
  const { id } = await params;
  const session = await requireAppSession(id, "api");
  const query = new URLSearchParams();
  for (const [key, value] of Object.entries(await searchParams)) {
    if (typeof value === "string") query.set(key, value);
  }
  let route: { method: string; path: string };
  let hostname: string;
  try {
    route = routeQuery(query);
    hostname = (await productControl().getApp(session.user.id, id)).hostname;
  } catch {
    notFound();
  }
  let initial: AppRouteDetail | null = null;
  try {
    initial = await appRouteDetail(session.user.id, id, route.method, route.path);
  } catch (error) {
    console.error("app_route_detail_unavailable", error);
    // The client keeps the page available and retries.
  }
  return (
    <ApiRouteDetail
      appId={id}
      method={route.method}
      path={route.path}
      origin={`https://${hostname}`}
      initial={initial}
      apiHref={`/apps/${id}/api`}
      tracesHrefBase={`/apps/${id}/traces?path=`}
    />
  );
}
