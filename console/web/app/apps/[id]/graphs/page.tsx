import { notFound, redirect } from "next/navigation";

import { APP_ID_PATTERN } from "@/lib/app-shell-data";

export const dynamic = "force-dynamic";

// The graphs merged into the App's home.
export default async function GraphsPage({ params }: { params: Promise<{ id: string }> }) {
  const { id } = await params;
  if (!APP_ID_PATTERN.test(id)) notFound();
  redirect(`/apps/${id}`);
}
