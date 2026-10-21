import { notFound } from "next/navigation";

import { requireAppSession } from "@/app/apps/[id]/section-data";
import { TraceExplorer } from "@/components/trace-explorer";
import { includeLinkedRequest } from "@/lib/linked-trace";
import { observationRepository } from "@core/observability/control";
import type { AppTraceTree } from "@core/observability/trace-tree";
import { productControl } from "@core/product-control";

export const dynamic = "force-dynamic";
export const metadata = { title: "Traces" };

export default async function TracesPage({
  params,
  searchParams,
}: {
  params: Promise<{ id: string }>;
  searchParams: Promise<{ path?: string | string[]; session?: string | string[]; request?: string | string[] }>;
}) {
  const { id } = await params;
  const { path, session: sessionID, request: requestedID } = await searchParams;
  const requestID = typeof requestedID === "string" && /^req_[0-9a-f]{32}$/.test(requestedID) ? requestedID : "";
  const session = await requireAppSession(id, "traces");
  try {
    await productControl().getApp(session.user.id, id);
  } catch {
    notFound();
  }
  let initial: AppTraceTree | null = null;
  const repository = observationRepository();
  try {
    initial = await repository.traceTree(id);
  } catch (error) {
    console.error("app_traces_unavailable", error);
    // The client keeps the section available and retries the isolated store.
  }
  if (requestID && !initial?.requests.some(({ id: seenID }) => seenID === requestID)) {
    try {
      const linked = await repository.requestTrace(id, requestID);
      initial = initial ? includeLinkedRequest(initial, linked) : linked;
    } catch (error) {
      console.error("app_linked_trace_unavailable", error);
      // The client retries the linked request with the rest of the trace tree.
    }
  }
  return <TraceExplorer appId={id} initial={initial} initialPath={typeof path === "string" ? path : ""} initialSession={typeof sessionID === "string" ? sessionID : ""} linkedRequestId={requestID} />;
}
