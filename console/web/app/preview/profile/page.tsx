import { notFound } from "next/navigation";

import { ProfileView } from "@/components/profile-view";
import type { CustomerApp } from "@/lib/customer-telemetry";
import type { Workspace, Sandbox } from "@/lib/telemetry-types";

// Development-only styling preview of the account profile page. Never served in production.
const apps: CustomerApp[] = [
  {
    role: "owner",
    owner: { email: "customer@example.com" },
    admin_count: 2,
    id: "app_0123456789abcdef0123456789abcdef",
    slug: "base-agent",
    hostname: "base-agent.cantelop.dev",
    created_at: "2026-08-01T12:00:00Z",
    updated_at: "2026-08-04T09:00:00Z",
  },
  {
    role: "admin",
    owner: { email: "maya@example.com" },
    admin_count: 1,
    id: "app_fedcba9876543210fedcba9876543210",
    slug: "image-catalog",
    hostname: "image-catalog.cantelop.dev",
    created_at: "2026-08-03T15:30:00Z",
    updated_at: "2026-08-03T15:30:00Z",
  },
];

const workspaces: Workspace[] = [
  {
    id: "wsp_base_agent_production",
    app_id: "app_0123456789abcdef0123456789abcdef",
    slug: "production",
    created_at: "2026-08-01T12:00:00Z",
    updated_at: "2026-08-01T12:00:00Z",
    last_activity_at: "2026-08-04T09:40:11Z",
  },
  {
    id: "wsp_base_agent_staging",
    app_id: "app_0123456789abcdef0123456789abcdef",
    slug: "staging",
    created_at: "2026-08-02T12:00:00Z",
    updated_at: "2026-08-02T12:00:00Z",
  },
  {
    id: "wsp_image_catalog_production",
    app_id: "app_fedcba9876543210fedcba9876543210",
    slug: "production",
    created_at: "2026-08-03T15:30:00Z",
    updated_at: "2026-08-03T15:30:00Z",
  },
];

const instance = (id: string, appId: string, workspaceId: string, vcpus: number, memoryMiB: number): Sandbox => ({
  id,
  workspace_id: workspaceId,
  app_id: appId,
  profile: "standard-2",
  build_id: "sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
  vcpus,
  memory_mib: memoryMiB,
  state: "running",
  created_at: "2026-08-04T08:12:03Z",
  updated_at: "2026-08-04T09:40:11Z",
});

// `?empty` renders the account as it looks right after sign-in — no apps;
// add `#example` to raise the use-case prompts, `#new-app` the create form.
export default async function ProfilePreviewPage({
  searchParams,
}: {
  searchParams: Promise<{ empty?: string | string[] }>;
}) {
  if (process.env.NODE_ENV === "production") notFound();
  const empty = (await searchParams).empty !== undefined;
  return (
    <ProfileView
      maxApps={5}
      usedApps={empty ? 0 : apps.filter((app) => app.role === "owner").length}
      viewer={{ login: "customer", email: "customer@example.com", avatarUrl: "https://avatars.githubusercontent.com/u/9919?s=64&v=4" }}
      initialApps={empty ? [] : apps}
      workspaces={empty ? [] : workspaces}
      runtime={Promise.resolve({
        instances: empty ? [] : [
          instance("sbx_01k1w9qf2m", "app_0123456789abcdef0123456789abcdef", "wsp_base_agent_production", 2, 2048),
          instance("sbx_01k1w8n3tc", "app_0123456789abcdef0123456789abcdef", "wsp_base_agent_production", 2, 2048),
        ],
        observedAt: "2026-08-04T09:41:52Z",
      })}
      // One app serving a build, one never deployed.
      deployments={empty ? {} : {
        app_0123456789abcdef0123456789abcdef: { version: 2, activated_at: "2026-08-04T08:06:44Z" },
        app_fedcba9876543210fedcba9876543210: null,
      }}
      unavailable={false}
    />
  );
}
