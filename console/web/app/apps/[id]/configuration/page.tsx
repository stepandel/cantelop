import { requireAppSession } from "@/app/apps/[id]/section-data";
import { ConfigPanel } from "@/components/config-panel";
import { SectionHead } from "@/components/section-head";
import { cachedCustomerConfig } from "@/lib/app-shell-data";
import type { ConfigValuePage } from "@/lib/config-values";

export const dynamic = "force-dynamic";

export const metadata = { title: "Environment" };

export default async function ConfigurationPage({ params }: { params: Promise<{ id: string }> }) {
  const { id } = await params;
  const session = await requireAppSession(id, "configuration");

  let config: ConfigValuePage = { items: [], declarations: null };
  let configUnavailable = false;
  try {
    config = await cachedCustomerConfig(session.user.id, "apps", id);
  } catch (error) {
    console.error("app_configuration_unavailable", error);
    configUnavailable = true;
  }

  return (
    <>
      {/* No count in the header: the panel adds and removes values in place,
          and a number up here would go stale the moment one lands. */}
      <SectionHead title="Environment" />
      <div className="section-panel">
        <ConfigPanel
          scope="apps"
          scopeId={id}
          scopeLabel="App"
          initialItems={config.items}
          declarations={config.declarations}
          unavailable={configUnavailable}
        />
      </div>
    </>
  );
}
