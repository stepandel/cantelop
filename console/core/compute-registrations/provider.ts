export interface ComputeAppRegistrationCommand {
 managed?:boolean;
  ownerUserID: string;
  appID: string;
  appSlug: string;
  internalPort: number;
}

export interface ComputeRegistrationProvider {
  registerApp(command: ComputeAppRegistrationCommand): Promise<void>;
  registerWorkspace(command: ComputeWorkspaceRegistrationCommand): Promise<void>;
  archiveWorkspace(command: ComputeWorkspaceArchiveCommand): Promise<void>;
  purgeWorkspace(command: ComputeWorkspacePurgeCommand): Promise<void>;
  archiveApp(command: ComputeAppArchiveCommand): Promise<void>;
  replaceConfiguration(command: ComputeConfigurationCommand): Promise<void>;
}

export interface ComputeConfigurationCommand {
  ownerUserID: string;
  appID: string;
  revision: number;
  values: Array<{ name: string; kind: "variable" | "secret"; value: string }>;
}

export interface ComputeWorkspaceRegistrationCommand {
  ownerUserID: string;
  appID: string;
  workspaceID: string;
  workspaceSlug: string;
  /** Authoritative registration context read by the control worker in one query. */
  registration?: { computeTenantID: string };
}

export interface ComputeAppArchiveCommand {
  ownerUserID: string;
  appID: string;
}

export interface ComputeWorkspaceArchiveCommand {
  ownerUserID: string;
  appID: string;
  workspaceID: string;
}

export type ComputeWorkspacePurgeCommand = ComputeWorkspaceArchiveCommand;

export class ComputeRegistrationProviderError extends Error {
  constructor(readonly retryable: boolean) {
    super("compute registration provider request failed");
    this.name = "ComputeRegistrationProviderError";
  }
}
