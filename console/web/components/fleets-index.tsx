"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect, useState, type FormEvent } from "react";

import type { CustomerFleet } from "@/lib/customer-fleets";
import { capacityLabel, fleetErrorMessage } from "@/lib/fleet-labels";
import { useClock } from "@/lib/use-clock";

/** Every Fleet the account owns, as one table, and the form that makes one.
 *  A Fleet takes one app slot when it is created, so at the account quota
 *  the form says so instead of opening. */
export function FleetsIndex({ fleets, atQuota }: { fleets: CustomerFleet[]; atQuota: boolean }) {
  const router = useRouter();
  const clock = useClock();
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [unlimited, setUnlimited] = useState(false);
  const [cap, setCap] = useState("100");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // The palette's "Create fleet" lands here with #new-fleet.
  useEffect(() => {
    const sync = () => { if (window.location.hash === "#new-fleet") setOpen(true); };
    sync();
    window.addEventListener("hashchange", sync);
    return () => window.removeEventListener("hashchange", sync);
  }, []);

  function close() {
    setOpen(false);
    setName("");
    setError(null);
    if (window.location.hash === "#new-fleet") history.replaceState(null, "", window.location.pathname);
  }

  async function create(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const response = await fetch("/api/customer/fleets", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name, max_apps: unlimited ? null : Number(cap) }),
      });
      const body = await response.json().catch(() => ({})) as { id?: string; error?: string };
      if (!response.ok || !body.id) {
        setError(fleetErrorMessage(body.error));
        return;
      }
      router.push(`/fleets/${body.id}`);
    } catch {
      setError("The control service is unavailable");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="section-panel">
      {fleets.length > 0 ? (
        <div className="fleet-table-wrap"><table className="data-table fleets-table">
          <colgroup>
            <col />
            <col className="c-count" />
            <col className="c-count" />
            <col className="c-stamp" />
          </colgroup>
          <thead>
            <tr>
              <th>Fleet</th>
              <th className="num" title="Installations against the cap">Installations</th>
              <th className="num" title="Active machine principals">Principals</th>
              <th>Created</th>
            </tr>
          </thead>
          <tbody>
            {fleets.map((fleet) => (
              <tr key={fleet.id} className="selectable" onClick={() => router.push(`/fleets/${fleet.id}`)}>
                <td className="ellip" title={fleet.id}>
                  <Link href={`/fleets/${fleet.id}`} onClick={(event) => event.stopPropagation()}>{fleet.name}</Link>
                </td>
                <td className={`num${fleet.max_apps !== null && fleet.used_apps >= fleet.max_apps ? " warn" : ""}`}>
                  {capacityLabel(fleet.used_apps, fleet.max_apps)}
                </td>
                <td className={`num${fleet.active_principals === 0 ? " faint" : ""}`}>{fleet.active_principals}</td>
                <td className="dim" title={clock.formatDate(fleet.created_at)}>{clock.formatStamp(fleet.created_at)}</td>
              </tr>
            ))}
          </tbody>
        </table></div>
      ) : (
        <p className="empty-note">
          No Fleets yet. A Fleet lets a service create isolated Apps for its own customers through
          scoped machine principals, using <code>/v1/fleets</code>.
        </p>
      )}
      {error && <div className="notice error" role="alert">{error}</div>}
      {atQuota ? (
        <p className="table-note">A Fleet uses one app slot, and your account quota is full.</p>
      ) : open ? (
        <form
          className="inline-form"
          id="new-fleet"
          onSubmit={(event) => void create(event)}
          onKeyDown={(event) => { if (event.key === "Escape") close(); }}
        >
          <label className="grow">Name
            <input
              autoFocus
              required
              pattern="[a-z][a-z0-9\-]{0,62}"
              maxLength={63}
              value={name}
              onChange={(event) => setName(event.target.value.toLowerCase())}
              placeholder="customer-agents"
            />
          </label>
          <label>Installation cap
            <input
              className="narrow"
              type="number"
              min={1}
              max={10000}
              required={!unlimited}
              disabled={unlimited}
              value={unlimited ? "" : cap}
              onChange={(event) => setCap(event.target.value)}
              placeholder="∞"
            />
          </label>
          <label className="check">
            <input type="checkbox" checked={unlimited} onChange={(event) => setUnlimited(event.target.checked)} />
            <span>Unlimited</span>
          </label>
          <button type="submit" disabled={busy || name.length === 0}>{busy ? "Creating…" : "Create fleet"}</button>
          <button type="button" className="button quiet" onClick={close} disabled={busy}>Cancel</button>
          <span className="form-hint">Uses one app slot however many Apps it installs.</span>
        </form>
      ) : (
        <div className="add-row">
          <button type="button" onClick={() => setOpen(true)}>
            <span aria-hidden="true">+</span> New fleet
          </button>
        </div>
      )}
    </div>
  );
}
