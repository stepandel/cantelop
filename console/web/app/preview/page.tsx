import { notFound } from "next/navigation";

import { AdminScreen } from "@/components/admin-screen";
import { AdminWorkspacesSection } from "@/components/admin-workspaces-section";
import { DashboardView } from "@/components/dashboard-view";
import { isAdminSectionId, type AdminSectionId } from "@/lib/admin-sections";
import type { Snapshot } from "@/lib/telemetry-types";
import type { UsageHistory, UsageSample } from "@/lib/usage-history";

export const dynamic = "force-dynamic";

// Deterministic synthetic sample window so the monitor panel can be styled
// without a live observer. Newest sample matches the snapshot's observed_at.
const buildSamples = (
  count: number,
  end: string,
  cpu: (i: number) => number,
  memory: (i: number) => number | undefined,
): UsageSample[] => {
  const endMS = Date.parse(end);
  return Array.from({ length: count }, (_, i): UsageSample => ({
    observed_at: new Date(endMS - (count - 1 - i) * 5_000).toISOString(),
    cpu_percent: i === 0 ? undefined : cpu(i),
    memory_bytes: memory(i) === undefined ? undefined : Math.round(memory(i)!),
  }));
};

type MemoryPreview = "missing" | "recovered" | undefined;

// ?memory=missing leaves the last three minutes blank; recovered adds a gap
// in the middle of the window to inspect disconnected lines and hover states.
const buildHistory = (memory: MemoryPreview): UsageHistory => ({
  "sbx_01k1w9qf2m": buildSamples(
    48,
    "2026-08-04T09:41:52Z",
    (i) => 22 + 14 * Math.sin(i / 4) + 9 * Math.sin(i / 1.7) + (i % 11 === 0 ? 26 : 0),
    (i) => i >= 12 && (memory === "missing" || (memory === "recovered" && i < 28))
      ? undefined
      : 640 * 1024 * 1024 + i * 1.9 * 1024 * 1024 + 22 * 1024 * 1024 * Math.sin(i / 5),
  ),
  "sbx_01k1w8n3tc": buildSamples(
    48,
    "2026-08-04T09:41:52Z",
    (i) => 6 + 3 * Math.sin(i / 3),
    (i) => 380 * 1024 * 1024 + 12 * 1024 * 1024 * Math.sin(i / 7),
  ),
});

// Development-only styling preview with fixture data. Never served in production.
const buildSnapshot = (memory: MemoryPreview): Snapshot => ({
  ready: true,
  observed_at: "2026-08-04T09:41:52Z",
  capacity: { warm_slots: { target: 16, ready: 13, claimed: 2, preparing: 1 } },
  // Deliberately unordered: the dashboard sorts by updated_at descending.
  workspaces: [],
  sessions: [
    {
      id: "ses_01k1w9qf2m",
      workspace_id: "wsp_base_agent",
      app_id: "base-agent",
      build_id: "sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
      keep_alive_seconds: 600,
		state: "active",
      created_at: "2026-08-04T08:12:03Z",
      updated_at: "2026-08-04T09:40:11Z",
    },
    {
      id: "ses_01k1w5rrd8",
      workspace_id: "wsp_base_agent",
      app_id: "base-agent",
      build_id: "sha256:fcde2b2edba56bf408601fb721fe9b5c338d10ee429ea04fae5511b68fbf8fb9",
      keep_alive_seconds: 300,
		state: "idle",
      created_at: "2026-08-04T06:02:41Z",
      updated_at: "2026-08-04T06:03:55Z",
    },
  ],
  sandboxes: [
    {
      id: "sbx_01k1wa02xr",
      workspace_id: "wsp_image_catalog",
      app_id: "image-catalog",
      profile: "large-4",
      build_id: "sha256:2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae",
      vcpus: 4,
      memory_mib: 8192,
      state: "running",
      created_at: "2026-08-04T09:38:59Z",
      updated_at: "2026-08-04T09:39:02Z",
    },
    {
      id: "sbx_01k1w9qf2m",
      workspace_id: "wsp_base_agent",
      app_id: "base-agent",
      profile: "standard-2",
      build_id: "sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
      vcpus: 2,
      memory_mib: 512,
      memory_max_mib: 2048,
      state: "running",
      startup: { completed_at: "2026-08-04T08:12:04Z", duration_ms: 1382.4 },
      usage: {
        observed_at: "2026-08-04T09:41:52Z",
        cpu_usage_us: 18422031,
        memory_current_bytes: 734003200,
        ...(memory === "missing" ? {} : { guest_memory_used_bytes: 314572800 }),
      },
      created_at: "2026-08-04T08:12:03Z",
      updated_at: "2026-08-04T09:40:11Z",
    },
    {
      id: "sbx_01k1w8n3tc",
      workspace_id: "wsp_base_agent",
      app_id: "base-agent",
      profile: "standard-2",
      build_id: "sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
      vcpus: 2,
      memory_mib: 2048,
      state: "running",
      startup: { completed_at: "2026-08-04T07:55:21Z", duration_ms: 917.62 },
      usage: {
        observed_at: "2026-08-04T09:41:52Z",
        cpu_usage_us: 8071992,
        memory_current_bytes: 402653184,
        guest_memory_used_bytes: 209715200,
      },
      created_at: "2026-08-04T07:55:20Z",
      updated_at: "2026-08-04T09:39:47Z",
    },
    {
      id: "sbx_01k1w5rrd8",
      workspace_id: "wsp_base_agent",
      app_id: "base-agent",
      profile: "standard-2",
      build_id: "sha256:fcde2b2edba56bf408601fb721fe9b5c338d10ee429ea04fae5511b68fbf8fb9",
      vcpus: 2,
      memory_mib: 2048,
      state: "terminated",
      created_at: "2026-08-04T06:02:41Z",
      updated_at: "2026-08-04T06:03:55Z",
    },
    {
      id: "sbx_01k1w2h9ka",
      workspace_id: "wsp_qualification",
      app_id: "qualification",
      profile: "standard-2",
      build_id: "sha256:fcde2b2edba56bf408601fb721fe9b5c338d10ee429ea04fae5511b68fbf8fb9",
      vcpus: 2,
      memory_mib: 4096,
      state: "terminated",
      startup: { completed_at: "2026-08-03T22:10:21Z", duration_ms: 2244.9 },
      created_at: "2026-08-03T22:10:18Z",
      updated_at: "2026-08-04T02:47:36Z",
    },
    {
      id: "sbx_01k1vzx4wp",
      workspace_id: "wsp_qualification",
      app_id: "qualification",
      profile: "standard-2",
      build_id: "sha256:fcde2b2edba56bf408601fb721fe9b5c338d10ee429ea04fae5511b68fbf8fb9",
      vcpus: 2,
      memory_mib: 4096,
      state: "terminated",
      created_at: "2026-08-03T20:33:07Z",
      updated_at: "2026-08-03T23:59:12Z",
    },
  ],
});

export default async function Preview({
  searchParams,
}: {
  searchParams: Promise<{ section?: string | string[]; memory?: string | string[] }>;
}) {
  if (process.env.NODE_ENV === "production") notFound();
  const { section: raw, memory: rawMemory } = await searchParams;
  const section: AdminSectionId = typeof raw === "string" && isAdminSectionId(raw) ? raw : "home";
  const memory: MemoryPreview = rawMemory === "missing" || rawMemory === "recovered" ? rawMemory : undefined;
  return (
    <AdminScreen
      viewer={{ login: "operator", email: "operator@example.com", avatarUrl: null }}
      snapshot={buildSnapshot(memory)}
      section={section}
      sectionHrefBase="/preview?section="
    >
    {(section === "home" || section === "sandboxes") && <DashboardView
      snapshot={buildSnapshot(memory)}
      unavailable={false}
      counts={{ users: 128, apps: 342 }}
      polling={false}
      canStopSandboxes
      initialHistory={buildHistory(memory)}
      initialSelectedId="sbx_01k1w9qf2m"
      view={section}
    />}
    {section === "workspaces" && <AdminWorkspacesSection workspaces={[
      { id: "wsp_3c1f0e9a7b2d4c6e8f0a1b2c3d4e5f60", slug: "staging", app_id: "app_9b8a7c6d5e4f30211203f4e5d6c7b8a9", app_slug: "image-catalog", status: "active", database: "provisioning", database_name: "ct-staging-3c1f0e9a7b2d4c6e8f0a1b2c3d4e5f60", created_at: "2026-08-04T08:12:31Z", archived_at: null },
      { id: "wsp_1a2b3c4d5e6f708192a3b4c5d6e7f801", slug: "production", app_id: "app_9b8a7c6d5e4f30211203f4e5d6c7b8a9", app_slug: "image-catalog", status: "active", database: "ready", database_name: "ct-staging-1a2b3c4d5e6f708192a3b4c5d6e7f801", created_at: "2026-08-02T17:40:05Z", archived_at: null },
      { id: "wsp_0f1e2d3c4b5a69788796a5b4c3d2e1f0", slug: "demo", app_id: "app_0a1b2c3d4e5f60718293a4b5c6d7e8f9", app_slug: "base-agent", status: "deletion-failed", database: "deleting", database_name: "ct-staging-0f1e2d3c4b5a69788796a5b4c3d2e1f0", created_at: "2026-07-21T11:03:47Z", archived_at: "2026-08-01T09:00:00Z" },
      { id: "wsp_7e6d5c4b3a29180f7e6d5c4b3a291807", slug: "default", app_id: "app_5f4e3d2c1b0a99887766554433221100", app_slug: "retired-demo", status: "unpurged", database: "ready", database_name: "ct-staging-7e6d5c4b3a29180f7e6d5c4b3a291807", created_at: "2026-06-11T09:20:00Z", archived_at: "2026-07-02T14:00:00Z" },
    ]} />}
    </AdminScreen>
  );
}
