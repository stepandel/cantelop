"use client";

import Link from "next/link";

import { CopyId } from "@/components/copy-id";
import type { FleetInstallationDetail as Detail } from "@/lib/customer-fleets";
import { formatMemory } from "@/lib/format";
import { formatCredits, formatStorage } from "@/lib/fleet-labels";
import { useClock } from "@/lib/use-clock";

/** One installation: its lifecycle, the policy its service set, and every
 *  budget epoch it was granted. Policy and budget are read-only here; the
 *  Fleet's service owns them through its principals. */
export function FleetInstallationDetail({ installation }: { installation: Detail }) {
  const clock = useClock();
  const { policy } = installation;
  const enforcing = installation.enforced_revision < installation.policy_revision;
  const expired = Date.parse(policy.expiresAt) <= Date.now();

  return (
    <>
      <div className="section-panel">
        {installation.error_code && (
          <div className="notice error" role="alert">Installation failed: <code>{installation.error_code}</code></div>
        )}
        <dl className="credential-facts">
          <div>
            <dt>App</dt>
            <dd>
              {installation.status === "deleted"
                ? <span className="dim">Deleted</span>
                : <Link href={`/apps/${installation.app_id}`}>Open app</Link>}
              <CopyId value={installation.app_id} />
            </dd>
          </div>
          <div>
            <dt>Package</dt>
            <dd>
              v{installation.version}
              {installation.next_version && <span className="info">upgrading to v{installation.next_version}</span>}
            </dd>
          </div>
          <div>
            <dt>Runtime</dt>
            <dd><span className="mono">{installation.runtime_id}</span></dd>
          </div>
          <div>
            <dt>Release</dt>
            <dd>{installation.release_status ?? <span className="dim">Not deployed yet</span>}</dd>
          </div>
          <div>
            <dt>Installed</dt>
            <dd title={clock.formatDate(installation.created_at)}>{clock.formatStamp(installation.created_at)}</dd>
          </div>
          <div>
            <dt>Updated</dt>
            <dd title={clock.formatDate(installation.updated_at)}>{clock.formatStamp(installation.updated_at)}</dd>
          </div>
        </dl>
      </div>

      <h2 className="section-label">Policy</h2>
      <div className="section-panel">
        <dl className="credential-facts">
          <div>
            <dt>Admission</dt>
            <dd>
              {!policy.active
                ? <span className="warn">Inactive</span>
                : expired
                  ? <span className="warn">Expired</span>
                  : <span className="ok">Active</span>}
              <span className="dim" title={clock.formatDate(policy.expiresAt)}>until {clock.formatStamp(policy.expiresAt)}</span>
            </dd>
          </div>
          <div><dt>Sandboxes</dt><dd>{policy.maxSandboxes}</dd></div>
          <div><dt>vCPUs</dt><dd>{policy.maxVcpus} <span className="dim">across all Sandboxes</span></dd></div>
          <div><dt>Memory</dt><dd>{formatMemory(policy.maxMemoryMib * 1024 * 1024)} <span className="dim">boot plus hotplug capacity</span></dd></div>
          <div><dt>Workspaces</dt><dd>{policy.maxWorkspaces}</dd></div>
          <div><dt>Storage</dt><dd>{formatStorage(policy.maxStorageBytes)}</dd></div>
          <div>
            <dt>Revision</dt>
            <dd>
              {installation.policy_revision}
              {enforcing
                ? <span className="info">enforcing · revision {installation.enforced_revision} is in force</span>
                : <span className="dim">enforced</span>}
            </dd>
          </div>
        </dl>
        <p className="table-note">Set by the Fleet&rsquo;s service with <code>policy:write</code>.</p>
      </div>

      <h2 className="section-label">Budget</h2>
      <div className="section-panel">
        {installation.budgets.length === 0 ? (
          <p className="empty-note">No budget granted. Without one, the installation cannot fund execution.</p>
        ) : (
          <div className="fleet-table-wrap"><table className="data-table fleet-budget-table">
            <colgroup>
              <col />
              <col className="c-count" />
              <col className="c-count" />
              <col className="c-count" />
              <col className="c-count" />
              <col className="c-stamp" />
            </colgroup>
            <thead>
              <tr>
                <th>Epoch</th>
                <th className="num">Included</th>
                <th className="num">Settled</th>
                <th className="num">Reserved</th>
                <th className="num">Available</th>
                <th>Expires</th>
              </tr>
            </thead>
            <tbody>
              {installation.budgets.map((epoch) => {
                const lapsed = Date.parse(epoch.expires_at) <= Date.now();
                return (
                  <tr key={epoch.epoch}>
                    <td className="ellip mono" title={epoch.epoch}>{epoch.epoch}</td>
                    <td className="num">{formatCredits(epoch.included)}</td>
                    <td className="num dim">{formatCredits(epoch.settled)}</td>
                    <td className="num dim">{formatCredits(epoch.reserved)}</td>
                    <td className={`num${epoch.available === "0" ? " warn" : ""}`}>{formatCredits(epoch.available)}</td>
                    <td className={lapsed ? "faint" : "dim"} title={clock.formatDate(epoch.expires_at)}>
                      {lapsed ? "Expired " : ""}{clock.formatStamp(epoch.expires_at)}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table></div>
        )}
        <p className="table-note">Credits. Granted by the Fleet&rsquo;s service with <code>policy:write</code>; settled as Sandboxes run.</p>
      </div>
    </>
  );
}
