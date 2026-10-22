import { controlDatabase } from "../db/database";
import { ConfigCipher } from "../configuration/cipher";
import { configuredTursoProvider } from "./provider";
import { WorkspaceDatabaseService } from "./service";
import { verifyAssignedSandbox, type SandboxDatabaseIdentity } from "./capability";
export function verifyRuntimeDatabaseSandbox(identity: SandboxDatabaseIdentity): Promise<void> {
  return verifyAssignedSandbox(controlDatabase(), identity);
}
export function workspaceDatabases(): WorkspaceDatabaseService {
  const environment = process.env.CANTELOP_TURSO_ENVIRONMENT?.trim();
  if (!environment) throw new Error("CANTELOP_TURSO_ENVIRONMENT is required");
  return new WorkspaceDatabaseService(
    controlDatabase(),
    configuredTursoProvider(),
    environment,
    ConfigCipher.configured(),
  );
}
