"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useMemo, type ReactNode } from "react";

import type { FleetInstallation, FleetPackage } from "@/lib/customer-fleets";
import { formatMemory } from "@/lib/format";
import { runningByApp } from "@/lib/instances";
import type { Sandbox } from "@/lib/telemetry-types";
import { useClock } from "@/lib/use-clock";
import { useStreamed } from "@/lib/use-streamed";

/** The installation's lifecycle word with its dot, as the App tables draw a
 *  release or Sandbox state. */
export function InstallationState({ status }: { status: FleetInstallation["status"] }) {
  return (
    <span className={`state-cell st-${status}`}>
      <span className={`dot dot-${status}`} aria-hidden="true" />
      {status}
    </span>
  );
}

/** What the service last let the installation use. An inactive or expired
 *  policy admits nothing, whatever its limits say. */
export function policySummary(installation: Pick<FleetInstallation, "policy">, now: number): string {
  const { policy } = installation;
  if (!policy.active) return "Inactive";
  if (Date.parse(policy.expiresAt) <= now) return "Expired";
  return `${policy.maxSandboxes} sbx · ${policy.maxVcpus} vcpu · ${formatMemory(policy.maxMemoryMib * 1024 * 1024)}`;
}

export function FleetInstallationsTable({ fleetId, installations, runtime, empty }: {
  fleetId: string;
  installations: FleetInstallation[];
  // The account's Sandboxes, streamed in after the first paint; null when
  // runtime state is unavailable.
  runtime: Promise<Sandbox[] | null>;
  empty?: ReactNode;
}) {
  const router = useRouter();
  const clock = useClock();
  const sandboxes = useStreamed(runtime);
  const running = useMemo(() => (sandboxes ? runningByApp(sandboxes) : null), [sandboxes]);
  if (installations.length === 0) {
    return (
      <p className="empty-note">
        {empty ?? (
          <>
            No installations. The Fleet&rsquo;s service installs Apps with a principal that holds{" "}
            <code>installations:write</code>.
          </>
        )}
      </p>
    );
  }
  const now = Date.now();
  return (
    <div className="fleet-table-wrap"><table className="data-table fleet-installations-table">
      <colgroup>
        <col />
        <col className="c-state" />
        <col className="c-sandboxes" />
        <col className="c-build" />
        <col className="c-shape" />
        <col className="c-kind" />
        <col className="c-stamp" />
      </colgroup>
      <thead>
        <tr>
          <th>External key</th>
          <th>Status</th>
          <th className="num" title="Running Sandboxes">Sandboxes</th>
          <th>Package</th>
          <th>Policy</th>
          <th title="Whether compute enforces the latest policy revision">Enforced</th>
          <th>Updated</th>
        </tr>
      </thead>
      <tbody>
        {installations.map((installation) => {
          const href = `/fleets/${fleetId}/installations/${installation.id}`;
          const enforcing = installation.enforced_revision < installation.policy_revision;
          const count = running?.get(installation.app_id) ?? 0;
          return (
            <tr key={installation.id} className="selectable" onClick={() => router.push(href)}>
              <td className="ellip mono" title={`${installation.external_key} · ${installation.id}`}>
                <Link href={href} onClick={(event) => event.stopPropagation()}>{installation.external_key}</Link>
              </td>
              <td title={installation.error_code ?? undefined}><InstallationState status={installation.status} /></td>
              <td className="num">
                {running === null
                  ? <span className="faint" title={sandboxes === undefined ? "Reading runtime state" : "Runtime state is unavailable"}>—</span>
                  : count === 0
                    ? <span className="faint">0</span>
                    : (
                      <Link href={`${href}/sandboxes`} onClick={(event) => event.stopPropagation()} title="Inspect running Sandboxes">
                        {count}
                      </Link>
                    )}
              </td>
              <td className="dim">
                v{installation.version}
                {installation.next_package_id && <span className="info"> ›</span>}
              </td>
              <td className={`dim${installation.policy.active ? "" : " faint"}`}>{policySummary(installation, now)}</td>
              <td className={enforcing ? "dim info" : "dim"} title={`Revision ${installation.policy_revision}, enforced ${installation.enforced_revision}`}>
                {enforcing ? "Pending" : "Yes"}
              </td>
              <td className="dim" title={clock.formatDate(installation.updated_at)}>{clock.formatStamp(installation.updated_at)}</td>
            </tr>
          );
        })}
      </tbody>
    </table></div>
  );
}

export function FleetPackagesTable({ packages }: { packages: FleetPackage[] }) {
  const clock = useClock();
  if (packages.length === 0) {
    return (
      <p className="empty-note">
        No packages. A principal with <code>packages:write</code> publishes a release of one of your Apps as a package.
      </p>
    );
  }
  return (
    <div className="fleet-table-wrap"><table className="data-table fleet-packages-table">
      <colgroup>
        <col />
        <col className="c-build" />
        <col className="c-id" />
        <col className="c-state" />
        <col className="c-stamp" />
      </colgroup>
      <thead>
        <tr>
          <th>Package</th>
          <th>Version</th>
          <th>Runtime</th>
          <th>Status</th>
          <th>Published</th>
        </tr>
      </thead>
      <tbody>
        {packages.map((pkg) => (
          <tr key={pkg.id}>
            <td className="ellip" title={pkg.id}>{pkg.name}</td>
            <td className="dim">v{pkg.version}</td>
            <td className="ellip mono dim" title={pkg.runtime_id}>{pkg.runtime_id}</td>
            <td>
              <span className={`state-cell st-${pkg.disabled_at ? "disabled" : "active"}`}>
                <span className={`dot dot-${pkg.disabled_at ? "disabled" : "active"}`} aria-hidden="true" />
                {pkg.disabled_at ? "disabled" : "available"}
              </span>
            </td>
            <td className="dim" title={clock.formatDate(pkg.created_at)}>{clock.formatStamp(pkg.created_at)}</td>
          </tr>
        ))}
      </tbody>
    </table></div>
  );
}
