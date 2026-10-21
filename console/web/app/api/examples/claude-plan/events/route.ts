import { type NextRequest, NextResponse } from "next/server";
import { admitDemoRequest, demoIdentity, demoSession } from "../shared";
import { eventsResponse, InvalidCursorError } from "../events-response";

export const dynamic = "force-dynamic";
export async function GET(request: NextRequest) {
  const site = request.headers.get("sec-fetch-site");
  if (site && site !== "same-origin" && site !== "none") return NextResponse.json({error:"invalid_request"},{status:400});
  try {
    const identity = await demoIdentity(request);
    if (!identity) return NextResponse.json({error:"Connect the demo before continuing."},{status:400});
    if (!await admitDemoRequest(identity)) return NextResponse.json({error:"The live demo request limit has been reached. Try again later."},{status:429});
    const session = demoSession(identity,request.nextUrl.searchParams.get("sessionId") ?? "");
    if (!session) return NextResponse.json({error:"Session not found"},{status:404});
    return eventsResponse(session,request);
  } catch (error) {
    if (error instanceof InvalidCursorError) return NextResponse.json({error:"invalid_request"},{status:400});
    return NextResponse.json({error:"The live deployment stream could not be reached."},{status:502});
  }
}
