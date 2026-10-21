import { NextResponse } from "next/server";

import { ControlError } from "../domain/errors";

export function productErrorResponse(error: unknown): NextResponse {
  if (error instanceof TypeError) return NextResponse.json({ error: "invalid_request" }, { status: 400 });
  if (error instanceof ControlError) {
    const status = {
      not_found: 404, conflict: 409, quota_exceeded: 429, user_suspended: 401,
      integration_not_configured: 404, integration_rotation_unsupported: 409, integration_rotation_unresolved: 409, release_unsupported: 409,
      target_build_missing: 410,
    }[error.code];
    return NextResponse.json({ error: error.code }, { status });
  }
  return NextResponse.json({ error: "control_unavailable" }, { status: 503 });
}

export function unauthorizedResponse(): NextResponse {
  return NextResponse.json({ error: "unauthorized" }, {
    status: 401,
    headers: { "WWW-Authenticate": 'Bearer realm="cantelop-control"' },
  });
}
