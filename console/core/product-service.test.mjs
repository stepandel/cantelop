import { expect, test } from "bun:test";

import { ProductService } from "./product-service";

const at = new Date("2026-08-12T12:00:00Z");
const userID = "usr_0123456789abcdef0123456789abcdef";
const app = {
  id: "app_0123456789abcdef0123456789abcdef",
  ownerUserID: userID,
  ownerEmail: "alice@example.com",
  adminCount: 2,
  slug: "agent",
  status: "active",
  sessionRuntime: { internalPort: 3000 },
  createdAt: at,
  updatedAt: at,
};
const workspace = {
  id: "wsp_0123456789abcdef0123456789abcdef",
  appID: app.id,
  slug: "production",
  createdAt: at,
  updatedAt: at,
};

test("projects product records into the public App contract", async () => {
  const service = new ProductService({ async listAppsForUser(id) {
    expect(id).toBe(userID);
    return [app];
  } });
  expect(await service.listApps(userID)).toEqual({ items: [{
    id: app.id,
    slug: "agent",
    hostname: "agent.cantelop.dev",
    role: "owner",
    owner: { email: "alice@example.com" },
    admin_count: 2,
    created_at: at.toISOString(),
    updated_at: at.toISOString(),
  }] });
});

test("resolves an App by slug without listing the tenant", async () => {
  const service = new ProductService({ async getAppBySlugForUser(id, slug) {
    expect(id).toBe(userID);
    expect(slug).toBe("agent");
    return app;
  } });
  expect(await service.getAppBySlug(userID, "agent")).toMatchObject({ id: app.id, slug: "agent" });
});

test("projects App-scoped Workspaces without a direct hostname", async () => {
  const service = new ProductService({
    // App visibility is enforced by the Workspace query itself.
    async listAppsForUser() { throw new Error("Workspace listing must not list Apps"); },
    async listWorkspacesForUser(id, appID) {
      expect(id).toBe(userID);
      expect(appID).toBe(app.id);
      return [workspace];
    },
  });
	expect(await service.listWorkspaces(userID, app.id)).toMatchObject({ items: [{ id: workspace.id }] });
	expect((await service.listWorkspaces(userID, app.id)).items[0]).not.toHaveProperty("session_runtime");
	expect((await service.listWorkspaces(userID, app.id)).items[0]).not.toHaveProperty("hostname");
});

test("creates an App-scoped Workspace for the trusted runtime bridge", async () => {
  const service = new ProductService({
    async getAppByID(id) {
      expect(id).toBe(app.id);
      return app;
    },
    async createWorkspaceForApp(input) {
      expect(input).toEqual({ appID: app.id, slug: "preview" });
      return { ...workspace, slug: input.slug };
    },
  });
	expect(await service.createWorkspaceForApp({ appID: app.id, slug: "preview" })).toMatchObject({
		app_id: app.id,
		slug: "preview",
	});
});

test("opens an App-scoped Workspace through the SDK materialization boundary", async () => {
  const service = new ProductService({
    async getAppByID() { return app; },
    async openWorkspaceForApp(input) { return { ...workspace, slug: input.slug }; },
  });
  expect(await service.openWorkspaceForApp({ appID: app.id, slug: "preview" })).toMatchObject({
    app_id: app.id,
    slug: "preview",
  });
});
