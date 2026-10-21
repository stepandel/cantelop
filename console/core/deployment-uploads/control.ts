import {configurationControl} from "../configuration/control";
import "server-only";

import { controlDatabase } from "../db/database";
import { resolveAppOwnerForUser } from "../db/app-access";
import { DeploymentUploadRepository } from "../db/deployment-upload-repository";
import { ReleaseRepository } from "../db/release-repository";
import { configuredDeploymentArtifactStore } from "./s3-artifact-store";
import { observedRetainedBuilds } from "./retained-builds";
import { DeploymentService } from "./service";

let sharedService: DeploymentService | undefined;

export function deploymentControl(): DeploymentService {
  if (!sharedService) {
    const pool = controlDatabase();
    sharedService = new DeploymentService(
      new DeploymentUploadRepository(pool),
      new ReleaseRepository(pool),
      configuredDeploymentArtifactStore(),
      undefined,
      (userID, appID) => resolveAppOwnerForUser(pool, userID, appID),
 (userID,appID,manifest)=>configurationControl().prepareIntegrationDeployment(userID,appID,manifest),
      (appID) => observedRetainedBuilds(appID),
    );
  }
  return sharedService;
}
