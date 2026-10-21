import "server-only";

import { configurationControl } from "@core/configuration/control";
import type { ConfigScope, ConfigValuePage } from "@/lib/config-values";

export async function getCustomerConfig(userID: string, scope: ConfigScope, scopeID: string): Promise<ConfigValuePage> {
  if (scope !== "apps") throw new Error("invalid configuration scope");
  return configurationControl().listForUser(userID, scopeID) as Promise<ConfigValuePage>;
}
