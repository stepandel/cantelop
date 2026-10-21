import { NextResponse } from "next/server";

import { getAdminSession } from "@/lib/admin-session";
import { isSameConsoleOrigin } from "@/lib/console-origin";
import { crashSandbox, terminateSandbox } from "@/lib/fire-fuse";

export const dynamic = "force-dynamic";

export async function DELETE(request: Request, context: { params: Promise<{ id: string }> }) {
  if (!(await getAdminSession())) return NextResponse.json({ error: "unauthorized" }, { status: 401 });
  if (!isSameConsoleOrigin(request.headers)) return NextResponse.json({ error: "invalid_request" }, { status: 400 });
  const { id } = await context.params;
  if (!/^sbx-[0-9a-f]{32}$/.test(id)) return NextResponse.json({ error: "not_found" }, { status: 404 });
  try {
    return NextResponse.json(await terminateSandbox(id), { headers: { "Cache-Control": "private, no-store" } });
  } catch (error) {
    const code = error instanceof Error ? error.message : "";
    const status = code === "runtime_sandbox_not_found" ? 404 : code === "runtime_sandbox_invalid_state" ? 409 : 502;
    return NextResponse.json({ error: status === 404 ? "not_found" : status === 409 ? "invalid_state" : code === "runtime_sandbox_unauthorized" ? "sandbox_stop_auth_unavailable" : "sandbox_stop_unavailable" }, { status });
  }
}

export async function POST(request: Request, context: { params: Promise<{ id: string }> }) {
  if (!(await getAdminSession())) return NextResponse.json({ error: "unauthorized" }, { status: 401 });
  if (!isSameConsoleOrigin(request.headers)) return NextResponse.json({ error: "invalid_request" }, { status: 400 });
  const { id } = await context.params;
  if (!/^sbx-[0-9a-f]{32}$/.test(id)) return NextResponse.json({ error: "not_found" }, { status: 404 });
  try {
    return NextResponse.json(await crashSandbox(id), { headers: { "Cache-Control": "private, no-store" } });
  } catch (error) {
    const code = error instanceof Error ? error.message : "";
    const status = code === "runtime_sandbox_not_found" ? 404 : code === "runtime_sandbox_invalid_state" ? 409 : 502;
    return NextResponse.json({ error: status === 404 ? "not_found" : status === 409 ? "invalid_state" : code === "runtime_sandbox_unauthorized" ? "sandbox_crash_auth_unavailable" : "sandbox_crash_unavailable" }, { status });
  }
}
