import { requireInstallation } from "@/app/fleets/[id]/installations/[installation]/section-data";
import { loadTraces, traceSearch, type TraceSearchParams } from "@/app/apps/[id]/section-data";
import { TraceExplorer } from "@/components/trace-explorer";

export const dynamic = "force-dynamic";
export const metadata = { title: "Traces" };

// The installation App's Requests and Messages, as its App's own traces page
// shows them, without leaving the Fleet.
export default async function FleetInstallationTracesPage({ params, searchParams }: {
  params: Promise<{ id: string; installation: string }>;
  searchParams: Promise<TraceSearchParams>;
}) {
  const { id, installation: installationID } = await params;
  const { installation } = await requireInstallation(id, installationID, "traces");
  if (!installation) return null;
  const search = traceSearch(await searchParams);
  const initial = await loadTraces(installation.app_id, search.requestID);
  return (
    <TraceExplorer
      appId={installation.app_id}
      initial={initial}
      initialPath={search.path}
      initialSession={search.session}
      linkedRequestId={search.requestID}
    />
  );
}
