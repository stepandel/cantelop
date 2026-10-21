import "server-only";

import { publicControlOrigin, tenantAccessToken, verifyCLIRefreshToken } from "@core/auth/tokens";
import { productControl } from "@core/product-control";
import type { ProductService } from "@core/product-service";

export function cliConfiguration() {
  return { control_url: publicControlOrigin() };
}

export async function exchangeCLICredential(refreshToken: string, control: ProductService = productControl()) {
  const userID = verifyCLIRefreshToken(refreshToken);
  const user = await control.assertActiveUser(userID);
  return {
    access_token: tenantAccessToken(user.id),
    token_type: "Bearer" as const,
    expires_in: 60,
    control_url: publicControlOrigin(),
  };
}
