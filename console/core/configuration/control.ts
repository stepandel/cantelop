import "server-only";

import { controlDatabase } from "../db/database";
import { ConfigCipher } from "./cipher";
import { ConfigRepository } from "./repository";

let shared: ConfigRepository | undefined;

export function configurationControl(): ConfigRepository {
  if (!shared) shared = new ConfigRepository(controlDatabase(), ConfigCipher.configured());
  return shared;
}
