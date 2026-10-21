import { NextResponse } from "next/server";

import { controlDatabase } from "@core/db/database";
import { checkWorkspaceDatabaseDrift } from "@core/workspace-databases/drift";
import { configuredTursoProvider } from "@core/workspace-databases/provider";
import { getAdminSession } from "@/lib/admin-session";

export const dynamic = "force-dynamic";

// On demand rather than at page load: it walks the whole Turso group.
export async function GET() {
  if (!(await getAdminSession())) return NextResponse.json({ error: "unauthorized" }, { status: 401 });
  const environment = process.env.CANTELOP_TURSO_ENVIRONMENT?.trim();
  let provider;
  try {
    if (!environment) throw new Error("CANTELOP_TURSO_ENVIRONMENT is required");
    provider = configuredTursoProvider();
  } catch (error) {
    console.error("database_provider_unconfigured", error);
    return NextResponse.json({ error: "database_provider_unconfigured" }, { status: 503 });
  }
  try {
    const drift = await checkWorkspaceDatabaseDrift(controlDatabase(), provider, environment);
    return NextResponse.json(drift, { headers: { "Cache-Control": "private, no-store" } });
  } catch (error) {
    console.error("drift_check_unavailable", error);
    return NextResponse.json({ error: "drift_check_unavailable" }, { status: 502 });
  }
}
