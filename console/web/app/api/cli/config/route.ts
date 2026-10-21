import { NextResponse } from "next/server";

import { cliConfiguration } from "@/lib/cli-auth";

export const dynamic = "force-dynamic";

export function GET() {
  try {
    const response = NextResponse.json(cliConfiguration());
    response.headers.set("Cache-Control", "public, max-age=300");
    return response;
  } catch (error) {
    console.error("cli_configuration_unavailable", error);
    return NextResponse.json({ error: "configuration_unavailable" }, { status: 503 });
  }
}
