import { type NextRequest, NextResponse } from "next/server";

import { fleetControl } from "@core/fleets/control";
import { fleetErrorResponse, fleetInput, fleetResponse, validFleetMutation } from "@/lib/fleet-http";
import { fleetCapacitySpec } from "@/lib/fleet-input";
import { getUserSession } from "@/lib/user-session";

export const dynamic = "force-dynamic";

export async function PATCH(request: NextRequest, context: { params: Promise<{ id: string }> }) {
  const session = await getUserSession();
  if (!session) return NextResponse.json({ error: "unauthorized" }, { status: 401 });
  if (!validFleetMutation(request)) return fleetResponse({ error: "invalid_request" }, 400);
  try {
    const { maxApps } = await fleetInput(request, fleetCapacitySpec);
    const updated = await fleetControl().updateCapacity(session.user.id, (await context.params).id, maxApps);
    return fleetResponse({ id: updated.id, max_apps: updated.max_apps });
  } catch (error) {
    return fleetErrorResponse(error);
  }
}
