import { requireInstallation } from "@/app/fleets/[id]/installations/[installation]/section-data";
import { loadRuntime } from "@/app/apps/[id]/section-data";
import { ObservabilityView } from "@/components/observability-view";

export const dynamic = "force-dynamic";
export const metadata = { title: "Sandboxes" };

// The installation App's Sandboxes, as its App's own monitoring page shows
// them, without leaving the Fleet.
export default async function FleetInstallationSandboxesPage({ params }: {
  params: Promise<{ id: string; installation: string }>;
}) {
  const { id, installation: installationID } = await params;
  const { session, installation } = await requireInstallation(id, installationID, "sandboxes");
  if (!installation) return null;
  const { workspaces, snapshot } = await loadRuntime(session.user.id, installation.app_id);
  return (
    <ObservabilityView
      appId={installation.app_id}
      workspaces={workspaces}
      snapshot={snapshot}
      unavailable={snapshot === null}
      view="sandboxes"
    />
  );
}
