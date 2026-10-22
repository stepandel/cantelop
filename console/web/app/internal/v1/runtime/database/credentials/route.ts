import { NextRequest, NextResponse } from "next/server";
import { workspaceDatabases, verifyRuntimeDatabaseSandbox } from "@core/workspace-databases/configured";
import { verifyDatabaseCapability, SandboxVerificationError } from "@core/workspace-databases/capability";
import { DatabaseAccessError } from "@core/workspace-databases/service";
import { DatabaseProviderError } from "@core/workspace-databases/provider";
export const dynamic = "force-dynamic";
export async function POST(request: NextRequest) {
  const headers = { "Cache-Control": "no-store" };
  let identity;
  try {
    if (request.nextUrl.search) throw new Error();
    identity = verifyDatabaseCapability(request.headers.get("authorization"), process.env.CANTELOP_WORKSPACE_DATABASE_CAPABILITY_KEY ?? "");
  } catch { return NextResponse.json({ error: { code: "unauthorized" } }, { status: 401, headers }); }
  let stage = "sandbox_verification";
  try {
    await verifyRuntimeDatabaseSandbox(identity);
    stage = "database_credentials";
    return NextResponse.json(await workspaceDatabases().applicationCredentials(identity.appId, identity.workspaceId), { headers });
  } catch (error) {
    // Only allowlisted codes and platform identities belong in diagnostics.
    // Exceptions, URLs, headers and provider response bodies can contain secrets.
    const { code, status } = diagnosticOf(error);
    console.warn("runtime_database_credentials_failed", {
      stage, sandbox_id: identity.sandboxId, app_id: identity.appId, workspace_id: identity.workspaceId, code, status,
    });
    return NextResponse.json({ error: { code: "database_access_unavailable" } }, { status: 503, headers });
  }
}
function diagnosticOf(error: unknown): { code: string; status?: number } {
  if (error instanceof SandboxVerificationError) return { code: error.code, status: error.status };
  if (error instanceof DatabaseProviderError) return { code: "database_provider_unavailable", status: error.status };
  if (error instanceof DatabaseAccessError) return { code: error.code };
  return { code: "database_credentials_unavailable" };
}
