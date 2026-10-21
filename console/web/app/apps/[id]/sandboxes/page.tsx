import { loadRuntime, requireAppSession } from "@/app/apps/[id]/section-data";
import { ObservabilityView } from "@/components/observability-view";

export const dynamic = "force-dynamic";
export const metadata = { title: "Sandboxes" };

export default async function AppSandboxesPage({ params }: { params: Promise<{ id: string }> }) {
  const { id } = await params;
  const session = await requireAppSession(id, "sandboxes");
  const { workspaces, snapshot } = await loadRuntime(session.user.id, id);
  return (
    <ObservabilityView
      appId={id}
      workspaces={workspaces}
      snapshot={snapshot}
      unavailable={snapshot === null}
      view="sandboxes"
    />
  );
}
