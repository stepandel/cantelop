import { requireAppSession } from "@/app/apps/[id]/section-data";
import { BuildsSection } from "@/components/builds-section";

export const dynamic = "force-dynamic";

export const metadata = { title: "Builds" };

// Build history rides the layout's own release poll — the section just reads
// it, so this page has nothing of its own to fetch.
export default async function BuildsPage({ params }: { params: Promise<{ id: string }> }) {
  const { id } = await params;
  await requireAppSession(id, "builds");
  return <BuildsSection />;
}
