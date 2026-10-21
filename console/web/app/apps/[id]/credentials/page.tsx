import { notFound } from "next/navigation";

import { requireAppSession } from "@/app/apps/[id]/section-data";
import { CredentialsPanel } from "@/components/credentials-panel";
import { SectionHead } from "@/components/section-head";
import { configurationControl } from "@core/configuration/control";
import { ControlError } from "@core/domain/errors";
import { publicCredentialState, type CredentialState } from "@/lib/integration-credentials";

export const dynamic = "force-dynamic";
export const metadata = { title: "Credentials" };

export default async function CredentialsPage({ params }: { params: Promise<{ id: string }> }) {
  const { id } = await params;
  const session = await requireAppSession(id, "credentials");
  let initial: CredentialState | null = null;
  try {
    initial = publicCredentialState(await configurationControl().integrationCredentialsForUser(session.user.id, id));
  } catch (error) {
    if (error instanceof ControlError && error.code === "integration_not_configured") initial = { issued: false };
    else if (error instanceof ControlError && error.code === "not_found") notFound();
    // Otherwise the panel retries from the client.
  }
  return (
    <>
      <SectionHead title="Credentials" />
      <CredentialsPanel appId={id} initial={initial} />
    </>
  );
}
