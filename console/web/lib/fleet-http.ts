import { NextResponse } from "next/server";

import { readBoundedUTF8Body } from "@core/http/bounded-body";
import { productErrorResponse } from "@core/http/response";
import { FleetError } from "@core/fleets/model";
import { isSameConsoleOrigin } from "./console-origin";

const maxBodyBytes = 16 * 1024;

/** A console write: same origin, and a JSON body when it carries one. */
export function validFleetMutation(request: Request, withBody = true): boolean {
  return isSameConsoleOrigin(request.headers) &&
    (!withBody || Boolean(request.headers.get("content-type")?.toLowerCase().startsWith("application/json")));
}

/** Parses and validates the body; any failure is one invalid_request. */
export async function fleetInput<T>(request: Request, spec: (input: unknown) => T): Promise<T> {
  try {
    const document = await readBoundedUTF8Body(request.body, 4 * maxBodyBytes);
    if (document.length === 0 || document.length > maxBodyBytes) throw new Error("invalid size");
    return spec(JSON.parse(document));
  } catch {
    throw new TypeError("invalid request");
  }
}

export function fleetResponse(body: unknown, status = 200) {
  return NextResponse.json(body, { status, headers: { "Cache-Control": "private, no-store" } });
}

export function fleetErrorResponse(error: unknown) {
  if (error instanceof FleetError) return fleetResponse({ error: error.code }, error.status);
  const response = productErrorResponse(error);
  response.headers.set("Cache-Control", "private, no-store");
  return response;
}
