import { type NextRequest, NextResponse } from "next/server";

import { fleetControl } from "@core/fleets/control";
import { fleetErrorResponse, fleetInput, fleetResponse, validFleetMutation } from "@/lib/fleet-http";
import { fleetCreateSpec } from "@/lib/fleet-input";
import { getUserSession } from "@/lib/user-session";

export const dynamic = "force-dynamic";

export async function POST(request: NextRequest) {
  const session = await getUserSession();
  if (!session) return NextResponse.json({ error: "unauthorized" }, { status: 401 });
  if (!validFleetMutation(request)) return fleetResponse({ error: "invalid_request" }, 400);
  try {
    const input = await fleetInput(request, fleetCreateSpec);
    const created = await fleetControl().create(session.user.id, input.name, input.maxApps);
    return fleetResponse({ id: created.id, name: created.name, max_apps: created.max_apps }, 201);
  } catch (error) {
    return fleetErrorResponse(error);
  }
}
