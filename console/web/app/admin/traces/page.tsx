import { requireAdminSession } from "@/app/admin/section-data";
import { TraceExplorer } from "@/components/trace-explorer";
import { observationRepository } from "@core/observability/control";
import type { AppTraceTree } from "@core/observability/trace-tree";

export const dynamic = "force-dynamic";
export const metadata = { title: "Traces" };

export default async function AdminTracesPage() {
  await requireAdminSession("traces");
  let appIds: string[] = [];
  let initial: AppTraceTree | null = { app_id: "", requests: [], unlinked_messages: [] };
  try {
    const repository = observationRepository();
    appIds = await repository.traceAppIDs();
    if (appIds[0]) initial = await repository.traceTree(appIds[0]);
  } catch (error) {
    console.error("admin_traces_unavailable", error);
    // The client keeps the section available and retries the isolated store.
    initial = null;
  }
  return <TraceExplorer appId={appIds[0] ?? ""} appIds={appIds} initial={initial} operator />;
}
