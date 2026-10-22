import { notFound } from "next/navigation";

import { loadTraces, requireAppSession, traceSearch, type TraceSearchParams } from "@/app/apps/[id]/section-data";
import { TraceExplorer } from "@/components/trace-explorer";
import { productControl } from "@core/product-control";

export const dynamic = "force-dynamic";
export const metadata = { title: "Traces" };

export default async function TracesPage({
  params,
  searchParams,
}: {
  params: Promise<{ id: string }>;
  searchParams: Promise<TraceSearchParams>;
}) {
  const { id } = await params;
  const search = traceSearch(await searchParams);
  const session = await requireAppSession(id, "traces");
  try {
    await productControl().getApp(session.user.id, id);
  } catch {
    notFound();
  }
  const initial = await loadTraces(id, search.requestID);
  return <TraceExplorer appId={id} initial={initial} initialPath={search.path} initialSession={search.session} linkedRequestId={search.requestID} />;
}
