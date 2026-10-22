import { type NextRequest, NextResponse } from "next/server";

import { fleetControl } from "@core/fleets/control";
import { identifier } from "@core/fleets/model";
import { fleetErrorResponse, fleetResponse, validFleetMutation } from "@/lib/fleet-http";
import { getUserSession } from "@/lib/user-session";

export const dynamic = "force-dynamic";

export async function DELETE(
  request: NextRequest,
  context: { params: Promise<{ id: string; principal_id: string }> },
) {
  const session = await getUserSession();
  if (!session) return NextResponse.json({ error: "unauthorized" }, { status: 401 });
  if (!validFleetMutation(request, false)) return fleetResponse({ error: "invalid_request" }, 400);
  try {
    const { id, principal_id: principalID } = await context.params;
    identifier(id, "flt");
    identifier(principalID, "fsp");
    await fleetControl().revoke(session.user.id, id, principalID);
    return fleetResponse({ revoked: true });
  } catch (error) {
    return fleetErrorResponse(error);
  }
}
