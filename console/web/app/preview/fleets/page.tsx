import { notFound } from "next/navigation";

import { ConsoleShell } from "@/components/console-shell";
import { CopyId } from "@/components/copy-id";
import { FleetCapacity } from "@/components/fleet-capacity";
import { FleetInstallationDetail } from "@/components/fleet-installation-detail";
import { FleetPrincipalsPanel } from "@/components/fleet-principals-panel";
import { FleetInstallationsTable, FleetPackagesTable, InstallationState } from "@/components/fleet-tables";
import { FleetsIndex } from "@/components/fleets-index";
import { HeadStat, SectionHead } from "@/components/section-head";
import type {
  CustomerFleet,
  FleetInstallation,
  FleetInstallationDetail as Detail,
  FleetPackage,
  FleetPrincipal,
} from "@/lib/customer-fleets";
import { capacityLabel } from "@/lib/fleet-labels";

export const dynamic = "force-dynamic";

// Development-only styling preview of the Fleet screens. Never served in
// production. `?view=index` (default), `?view=fleet` or `?view=installation`;
// `?empty` renders each with nothing in it.
const viewer = { login: "customer", email: "customer@example.com", avatarUrl: null };
const fleetID = "flt_0123456789abcdef0123456789abcdef";

const fleets: CustomerFleet[] = [
  { id: fleetID, name: "customer-agents", max_apps: null, used_apps: 1284, active_principals: 2, created_at: "2026-08-02T10:00:00Z" },
  { id: "flt_fedcba9876543210fedcba9876543210", name: "trial-sandboxes", max_apps: 50, used_apps: 50, active_principals: 1, created_at: "2026-08-05T16:20:00Z" },
];

const policy = {
  active: true,
  expiresAt: "2027-01-01T00:00:00Z",
  maxSandboxes: 4,
  maxVcpus: 8,
  maxMemoryMib: 16384,
  maxWorkspaces: 10,
  maxStorageBytes: 50 * 1024 ** 3,
};

const installation = (
  n: number,
  status: FleetInstallation["status"],
  extra: Partial<FleetInstallation> = {},
): FleetInstallation => ({
  id: `ins_${String(n).padStart(32, "0")}`,
  external_key: `acct:org_${String(n).padStart(6, "0")}:primary`,
  app_id: `app_${String(n).padStart(32, "a")}`,
  package_id: "pkg_0123456789abcdef0123456789abcdef",
  version: "1.4.0",
  next_package_id: null,
  status,
  error_code: null,
  release_status: status === "ready" ? "active" : "pending",
  policy,
  policy_revision: 3,
  enforced_revision: 3,
  created_at: "2026-08-06T09:00:00Z",
  updated_at: "2026-08-07T14:12:31Z",
  ...extra,
});

const installations: FleetInstallation[] = [
  installation(1, "ready"),
  installation(2, "ready", { policy_revision: 4 }),
  installation(3, "upgrading", { next_package_id: "pkg_fedcba9876543210fedcba9876543210" }),
  installation(4, "provisioning", { policy: { ...policy, active: false } }),
  installation(5, "failed", { error_code: "release_failed", release_status: "failed" }),
  installation(6, "suspended"),
  installation(7, "deleting"),
];

const principals: FleetPrincipal[] = [
  { id: "fsp_0123456789abcdef0123456789abcdef", label: "provisioning-api", scopes: ["installations:read", "installations:write", "credentials:read"], expires_at: "2027-02-01T00:00:00Z", revoked_at: null, created_at: "2026-08-02T10:05:00Z" },
  { id: "fsp_1123456789abcdef0123456789abcdef", label: "billing-sync", scopes: ["policy:write", "usage:read"], expires_at: "2027-02-01T00:00:00Z", revoked_at: null, created_at: "2026-08-02T10:06:00Z" },
  { id: "fsp_2123456789abcdef0123456789abcdef", label: "ci-publisher", scopes: ["packages:write"], expires_at: "2026-09-01T00:00:00Z", revoked_at: "2026-08-20T12:00:00Z", created_at: "2026-08-02T10:07:00Z" },
];

const packages: FleetPackage[] = [
  { id: "pkg_fedcba9876543210fedcba9876543210", name: "juiced", version: "1.5.0", runtime_id: "runtime_juiced_v15", disabled_at: null, created_at: "2026-08-07T12:00:00Z" },
  { id: "pkg_0123456789abcdef0123456789abcdef", name: "juiced", version: "1.4.0", runtime_id: "runtime_juiced_v14", disabled_at: null, created_at: "2026-08-03T12:00:00Z" },
  { id: "pkg_0023456789abcdef0123456789abcdef", name: "juiced", version: "1.3.2", runtime_id: "runtime_juiced_v13", disabled_at: "2026-08-04T12:00:00Z", created_at: "2026-08-01T12:00:00Z" },
];

const detail: Detail = {
  ...installation(2, "ready", { policy_revision: 4 }),
  next_version: null,
  runtime_id: "runtime_juiced_v14",
  budgets: [
    { epoch: "2026-08", included: "5000000", settled: "1832250", reserved: "120000", available: "3047750", expires_at: "2026-09-01T00:00:00Z" },
    { epoch: "2026-07", included: "5000000", settled: "5000000", reserved: "0", available: "0", expires_at: "2026-08-01T00:00:00Z" },
  ],
};

export default async function FleetsPreviewPage({ searchParams }: {
  searchParams: Promise<{ view?: string | string[]; empty?: string | string[] }>;
}) {
  if (process.env.NODE_ENV === "production") notFound();
  const params = await searchParams;
  const empty = params.empty !== undefined;
  const view = params.view === "fleet" || params.view === "installation" ? params.view : "index";
  const fleet = fleets[0]!;
  const shellFleets = fleets.map(({ id, name }) => ({ id, name }));

  if (view === "index") {
    return (
      <ConsoleShell viewer={viewer} fleets={shellFleets} maxApps={5} usedApps={3} activeNav="fleets" crumbs={[{ label: "Home", href: "/profile" }, { label: "Fleets" }]}>
        <SectionHead title="Fleets" subtitle="Isolated Apps your service installs for its own customers">
          <HeadStat label="Fleets" value={empty ? 0 : fleets.length} />
          <HeadStat label="Installations" value={empty ? 0 : fleets.reduce((total, item) => total + item.used_apps, 0)} />
        </SectionHead>
        <FleetsIndex fleets={empty ? [] : fleets} atQuota={false} />
      </ConsoleShell>
    );
  }
  if (view === "installation") {
    return (
      <ConsoleShell
        viewer={viewer}
        fleets={shellFleets}
        maxApps={5}
        usedApps={3}
        activeNav="fleets"
        crumbs={[{ label: "Home", href: "/profile" }, { label: "Fleets", href: "/fleets" }, { label: fleet.name, href: "/preview/fleets?view=fleet" }, { label: detail.external_key }]}
      >
        <SectionHead title={detail.external_key} subtitle={<CopyId value={detail.id} />}>
          <HeadStat label="Status" value={<InstallationState status={detail.status} />} />
          <HeadStat label="Package" value={`v${detail.version}`} />
        </SectionHead>
        <FleetInstallationDetail installation={empty ? { ...detail, budgets: [] } : detail} />
      </ConsoleShell>
    );
  }
  return (
    <ConsoleShell
      viewer={viewer}
      fleets={shellFleets}
      maxApps={5}
      usedApps={3}
      activeNav="fleets"
      crumbs={[{ label: "Home", href: "/profile" }, { label: "Fleets", href: "/preview/fleets" }, { label: fleet.name }]}
    >
      <SectionHead title={fleet.name} subtitle={<CopyId value={fleet.id} />}>
        <HeadStat label="Installations" value={capacityLabel(fleet.used_apps, fleet.max_apps)} />
        <HeadStat label="Principals" value={fleet.active_principals} />
      </SectionHead>
      <div className="section-panel">
        <FleetCapacity fleetId={fleet.id} maxApps={fleet.max_apps} usedApps={fleet.used_apps} createdAt={fleet.created_at} />
      </div>
      <h2 className="section-label">Installations</h2>
      <div className="section-panel">
        <FleetInstallationsTable fleetId={fleet.id} installations={empty ? [] : installations} />
      </div>
      <h2 className="section-label">Principals</h2>
      <div className="section-panel">
        <FleetPrincipalsPanel fleetId={fleet.id} initialPrincipals={empty ? [] : principals} />
      </div>
      <h2 className="section-label">Packages</h2>
      <div className="section-panel">
        <FleetPackagesTable packages={empty ? [] : packages} />
      </div>
    </ConsoleShell>
  );
}
