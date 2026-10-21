import { ControlError } from "../domain/errors";
import type { ProductService } from "../product-service";
import { productControl } from "../product-control";
import type { IdentityProvider } from "./providers";

export function resolveUserIdentity(
  provider: IdentityProvider,
  providerSubject: string,
  email: string,
  control: ProductService = productControl(),
) {
  return control.resolveIdentity({ provider, providerSubject, email });
}

/** Refresh a previously authenticated subject, including a recorded account merge. */
export async function resolveSessionIdentity(userID: string, control: ProductService = productControl()) {
  try {
    return await control.assertActiveUser(userID);
  } catch (error) {
    if (error instanceof ControlError && (error.code === "not_found" || error.code === "user_suspended")) return null;
    throw error;
  }
}
