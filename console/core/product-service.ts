import type {
  AppAccess,
  ProductApp,
  ProductWorkspace,
  ProductRepository,
  ProductUser,
} from "./db/product-repository";
import type { CreateAppInput, CreateWorkspaceInput, IdentityInput } from "./domain/model";
import { waitForWorkspaceHostArchive } from "./workspace-host-archive";

export interface PublicApp {
  id: string;
  slug: string;
  hostname: string;
  role: "owner" | "admin";
  /** The account the App belongs to; present on reads scoped to a user. */
  owner?: { email: string };
  /** Admin collaborators the owner has added. */
  admin_count: number;
  /** The Fleet that installed the App; null for a standalone App. */
  fleet_id: string | null;
  created_at: string;
  updated_at: string;
}

export interface PublicWorkspace {
  id: string;
  app_id: string;
  slug: string;
  created_at: string;
  updated_at: string;
  archived_at?: string;
}

export class ProductService {
  constructor(private readonly products: ProductRepository) {}

  resolveIdentity(input: IdentityInput): Promise<ProductUser> {
    return this.products.resolveIdentity(input);
  }

  assertActiveUser(userID: string): Promise<ProductUser> {
    return this.products.getActiveUser(userID);
  }

  getUserQuota(userID: string) {
    return this.products.getUserQuota(userID);
  }

  async listApps(userID: string): Promise<{ items: PublicApp[] }> {
    const apps = await this.products.listAppsForUser(userID);
    return { items: apps.map(publicApp) };
  }

  async getApp(userID: string, appID: string): Promise<PublicApp> {
    return publicApp(await this.products.getAppForUser(userID, appID));
  }

  resolveAppOwner(userID: string, appID: string): Promise<string> {
    return this.products.resolveAppOwnerForUser(userID, appID);
  }

  /** The App and its tenant owner in one read, for callers that need both. */
  async getAccessibleApp(userID: string, appID: string): Promise<{ app: PublicApp; ownerUserID: string }> {
    const app = await this.products.getAppForUser(userID, appID);
    return { app: publicApp(app), ownerUserID: app.ownerUserID };
  }

  async getAppBySlug(userID: string, slug: string): Promise<PublicApp> {
    return publicApp(await this.products.getAppBySlugForUser(userID, slug));
  }

  async createApp(userID: string, input: CreateAppInput): Promise<PublicApp> {
    return publicApp(await this.products.createAppForUser(userID, input));
  }

  archiveApp(userID: string, appID: string): Promise<void> {
    return this.products.archiveAppForUser(userID, appID);
  }

  getAppAccess(userID: string, appID: string): Promise<AppAccess> {
    return this.products.getAppAccessForUser(userID, appID);
  }

  removeAppAdmin(userID: string, appID: string, collaboratorUserID: string): Promise<void> {
    return this.products.removeAppAdminForOwner(userID, appID, collaboratorUserID);
  }

  // The repository query already limits Workspaces to accessible, unarchived
  // Apps, so no separate App listing is needed to vouch for them.
  async listWorkspaces(userID: string, appID?: string): Promise<{ items: PublicWorkspace[] }> {
    const workspaces = await this.products.listWorkspacesForUser(userID, appID);
    return { items: workspaces.map(publicWorkspace) };
  }

  async createWorkspace(userID: string, input: CreateWorkspaceInput): Promise<PublicWorkspace> {
    await this.products.getAppForUser(userID, input.appID);
    return publicWorkspace(await this.products.createWorkspaceForUser(userID, input));
  }

  async createWorkspaceForApp(input: CreateWorkspaceInput): Promise<PublicWorkspace> {
    await this.products.getAppByID(input.appID);
    return publicWorkspace(await this.products.createWorkspaceForApp(input));
  }

  /** Resolves false when the compute host has not archived the Workspace yet. */
  async deleteWorkspace(userID: string, workspaceID: string): Promise<boolean> {
    await this.products.deleteWorkspaceForUser(userID, workspaceID);
    return waitForWorkspaceHostArchive(this.products, workspaceID);
  }

  /** Does not wait for host archival: the App route acknowledges with 202 and
   *  callers observe progress through Workspace inspection. */
  async deleteWorkspaceForApp(appID: string, workspaceID: string): Promise<boolean> {
    await this.products.deleteWorkspaceForApp(appID, workspaceID);
    return this.products.isWorkspaceHostArchived(workspaceID);
  }

  async openWorkspaceForApp(input: CreateWorkspaceInput): Promise<PublicWorkspace> {
    await this.products.getAppByID(input.appID);
    return publicWorkspace(await this.products.openWorkspaceForApp(input));
  }
}

function publicApp(app: ProductApp): PublicApp {
  return {
    id: app.id,
    slug: app.slug,
    hostname: `${app.slug}.cantelop.dev`,
    role: app.accessRole ?? "owner",
    ...(app.ownerEmail !== undefined ? { owner: { email: app.ownerEmail } } : {}),
    admin_count: app.adminCount ?? 0,
    fleet_id: app.fleetID ?? null,
    created_at: app.createdAt.toISOString(),
    updated_at: app.updatedAt.toISOString(),
  };
}

function publicWorkspace(workspace: ProductWorkspace): PublicWorkspace {
  return {
    id: workspace.id,
    app_id: workspace.appID,
    slug: workspace.slug,
    created_at: workspace.createdAt.toISOString(),
    updated_at: workspace.updatedAt.toISOString(),
    ...(workspace.archivedAt ? { archived_at: workspace.archivedAt.toISOString() } : {}),
  };
}
