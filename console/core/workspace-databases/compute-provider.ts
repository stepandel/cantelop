import type { ComputeRegistrationProvider } from "../compute-registrations/provider";
import type { WorkspaceDatabaseService } from "./service";

// SDK 1.0 keeps Session mailboxes in the platform, so registering a Workspace
// never waits on its database: the application database is provisioned on
// first use, and purged with the Workspace.
export function withWorkspaceDatabases(compute: ComputeRegistrationProvider, databases: Pick<WorkspaceDatabaseService, "purge">): ComputeRegistrationProvider {
  return {
    registerApp: command => compute.registerApp(command),
    archiveApp: command => compute.archiveApp(command),
    archiveWorkspace: command => compute.archiveWorkspace(command),
    replaceConfiguration: command => compute.replaceConfiguration(command),
    registerWorkspace: command => compute.registerWorkspace(command),
    async purgeWorkspace(command) {
      await compute.purgeWorkspace(command);
      await databases.purge(command.workspaceID);
    },
  };
}
