import { type NextRequest, NextResponse } from "next/server";

import { fleetControl } from "@core/fleets/control";
import { fleetErrorResponse, fleetInput, fleetResponse, validFleetMutation } from "@/lib/fleet-http";
import { principalSpec } from "@/lib/fleet-input";
import { getUserSession } from "@/lib/user-session";

export const dynamic = "force-dynamic";

// The only response that ever carries the principal's secret; only its hash
// is stored.
export async function POST(request: NextRequest, context: { params: Promise<{ id: string }> }) {
  const session = await getUserSession();
  if (!session) return NextResponse.json({ error: "unauthorized" }, { status: 401 });
  if (!validFleetMutation(request)) return fleetResponse({ error: "invalid_request" }, 400);
  try {
    const input = await fleetInput(request, (body) => principalSpec(body));
    return fleetResponse(await fleetControl().principal(session.user.id, (await context.params).id, input), 201);
  } catch (error) {
    return fleetErrorResponse(error);
  }
}
