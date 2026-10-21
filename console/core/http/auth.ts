import { verifyTenantAccessToken, type TenantScope } from "../auth/tokens";

import { ControlError } from "../domain/errors";
import { productControl } from "../product-control";
import type { ProductService } from "../product-service";
import { productErrorResponse, unauthorizedResponse } from "./response";

class InvalidAccessCredential extends Error {}

export async function authenticatedUserID(authorization: string | null, scope: TenantScope, control?: ProductService): Promise<string> {
  if (!authorization?.startsWith("Bearer ") || authorization.length <= "Bearer ".length || authorization.includes(",")) {
    throw new InvalidAccessCredential("invalid access credential");
  }
  let subject: string;
  try { subject = verifyTenantAccessToken(authorization.slice("Bearer ".length), scope); }
  catch { throw new InvalidAccessCredential("invalid access credential"); }
  return (await (control ?? productControl()).assertActiveUser(subject)).id;
}

export function authenticationErrorResponse(error: unknown) {
  if (error instanceof InvalidAccessCredential ||
      error instanceof ControlError && (error.code === "not_found" || error.code === "user_suspended")) {
    return unauthorizedResponse();
  }
  return productErrorResponse(error);
}
