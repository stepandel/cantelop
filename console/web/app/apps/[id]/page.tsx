import { loadRuntime, requireAppSession } from "@/app/apps/[id]/section-data";
import { ObservabilityView } from "@/components/observability-view";

export const dynamic = "force-dynamic";

export const metadata = { title: "App" };

// The App's home: the layout's AppScreen carries the App-wide header (URL,
// status, build, deployed, sandboxes) here, and the body is the runtime
// graphs — the first thing to look at when opening an App.
export default async function AppHomePage({ params }: { params: Promise<{ id: string }> }) {
  const { id } = await params;
  const session = await requireAppSession(id);
  const { workspaces, snapshot } = await loadRuntime(session.user.id, id);
  return (
    <ObservabilityView
      appId={id}
      workspaces={workspaces}
      snapshot={snapshot}
      unavailable={snapshot === null}
      view="graphs"
      showHead={false}
    />
  );
}
