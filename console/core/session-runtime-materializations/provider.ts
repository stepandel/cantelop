import type { PreparedManifest } from "./prepared-artifact-store";

export type SessionRuntimeMaterializationStatus = "receiving" | "succeeded" | "failed";

export interface SessionRuntimeMaterialization {
  operationID: string;
  status: SessionRuntimeMaterializationStatus;
  buildID?: string;
  buildVersion?: number;
  errorCode?: string;
}

export interface SessionRuntimeMaterializationCommand {
  commandID: string;
  ownerUserID: string;
  appID: string;
  imageURL: string;
  manifest: PreparedManifest;
}

export interface SessionRuntimeProvider {
  materialize(command: SessionRuntimeMaterializationCommand): Promise<SessionRuntimeMaterialization>;
  selectBuild(ownerUserID: string, appID: string, buildID: string | null): Promise<void>;
  activeBuild(ownerUserID: string, appID: string): Promise<string | null>;
  /** Whether the host still retains the Build for the App, so it can be selected. */
  retainsBuild?(ownerUserID: string, appID: string, buildID: string): Promise<boolean>;
  releaseAdmission(ownerUserID: string, appID: string): Promise<{ generation: string; closed: boolean }>;
  setReleaseAdmission(ownerUserID: string, appID: string, generation: string, closed: boolean): Promise<void>;
  cancel?(ownerUserID: string, appID: string, commandID: string): Promise<void>;
}

export class SessionRuntimeProviderError extends Error {
  constructor(readonly retryable: boolean, readonly errorCode?: "build_retention_quota" | "host_disk_capacity", readonly progress = "provider_unavailable") {
    super("sessionRuntime provider request failed");
    this.name = "SessionRuntimeProviderError";
  }
}
