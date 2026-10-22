"use client";

import { useRouter } from "next/navigation";
import { useEffect, useState, type FormEvent } from "react";

import { scopes, type FleetScope } from "@core/fleets/scopes";
import type { FleetPrincipal } from "@/lib/customer-fleets";
import { fleetErrorMessage, principalState } from "@/lib/fleet-labels";
import { useClock } from "@/lib/use-clock";
import { useCopy } from "@/lib/use-copy";

// What each scope lets the Fleet's service do, for the create form's titles.
const scopeHelp: Record<FleetScope, string> = {
  "packages:write": "Publish and disable packages",
  "installations:write": "Install, upgrade and delete Apps",
  "installations:read": "Read installations and packages",
  "credentials:read": "Read an installation's App credential",
  "policy:write": "Set policy and grant budgets",
  "usage:read": "Read budgets and the usage cursor",
};

/** The machine principals that act for one Fleet. The owner creates one with
 *  its scopes and lifetime, sees its secret exactly once, and revokes it from
 *  its row; an expired or revoked principal stays listed as history. */
export function FleetPrincipalsPanel({ fleetId, initialPrincipals }: {
  fleetId: string;
  initialPrincipals: FleetPrincipal[];
}) {
  const router = useRouter();
  const clock = useClock();
  const { copied, copy, reset } = useCopy();
  const [principals, setPrincipals] = useState(initialPrincipals);
  useEffect(() => { setPrincipals(initialPrincipals); }, [initialPrincipals]);
  const [now, setNow] = useState<number | null>(null);
  useEffect(() => {
    setNow(Date.now());
    const timer = setInterval(() => setNow(Date.now()), 30_000);
    return () => clearInterval(timer);
  }, []);
  const [open, setOpen] = useState(false);
  const [label, setLabel] = useState("");
  const [chosen, setChosen] = useState<FleetScope[]>([]);
  const [days, setDays] = useState("90");
  const [busy, setBusy] = useState<string | null>(null);
  const [confirmingID, setConfirmingID] = useState<string | null>(null);
  const [message, setMessage] = useState<{ kind: "error" | "status"; content: string } | null>(null);
  // The one-time secret. It lives only until the owner dismisses it.
  const [secret, setSecret] = useState<{ label: string; token: string } | null>(null);

  function close() {
    setOpen(false);
    setLabel("");
    setChosen([]);
    setDays("90");
  }

  function dismissSecret() {
    setSecret(null);
    reset();
  }

  async function create(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setBusy("create");
    setMessage(null);
    try {
      const response = await fetch(`/api/customer/fleets/${fleetId}/principals`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ label, scopes: chosen, expires_in_days: Number(days) }),
      });
      const body = await response.json().catch(() => ({})) as { token?: string; error?: string };
      if (!response.ok || !body.token) {
        setMessage({ kind: "error", content: fleetErrorMessage(body.error) });
        return;
      }
      setSecret({ label: label.trim(), token: body.token });
      close();
      router.refresh();
    } catch {
      setMessage({ kind: "error", content: "The control service is unavailable" });
    } finally {
      setBusy(null);
    }
  }

  async function revoke(principal: FleetPrincipal) {
    setBusy(principal.id);
    setMessage(null);
    try {
      const response = await fetch(`/api/customer/fleets/${fleetId}/principals/${principal.id}`, { method: "DELETE" });
      if (!response.ok) {
        const body = await response.json().catch(() => ({})) as { error?: string };
        setMessage({ kind: "error", content: fleetErrorMessage(body.error) });
        return;
      }
      setPrincipals((current) => current.map((item) =>
        item.id === principal.id ? { ...item, revoked_at: new Date().toISOString() } : item));
      setMessage({ kind: "status", content: `${principal.label} revoked — its secret stops working immediately` });
      router.refresh();
    } catch {
      setMessage({ kind: "error", content: "The control service is unavailable" });
    } finally {
      setBusy(null);
      setConfirmingID(null);
    }
  }

  function toggle(scope: FleetScope, on: boolean) {
    setChosen((current) => on ? [...current, scope] : current.filter((item) => item !== scope));
  }

  return (
    <>
      {secret && (
        <div className="notice status fleet-secret" role="status">
          <p>
            Secret for <strong>{secret.label}</strong>. Copy it now: it is shown only once, and only its hash is stored.
          </p>
          <code>{secret.token}</code>
          <div className="fleet-secret-actions">
            <button type="button" className="button small" onClick={() => void copy(secret.token)}>{copied ? "Copied" : "Copy"}</button>
            <button type="button" className="button quiet small" onClick={dismissSecret}>Done</button>
          </div>
        </div>
      )}
      {message && (
        <div className={`notice ${message.kind}`} role={message.kind === "error" ? "alert" : "status"}>{message.content}</div>
      )}
      {principals.length > 0 ? (
        <div className="fleet-table-wrap"><table className="data-table fleet-principals-table">
          <colgroup>
            <col className="c-workspace" />
            <col />
            <col className="c-status" />
            <col className="c-actions" />
          </colgroup>
          <thead>
            <tr>
              <th>Label</th>
              <th>Scopes</th>
              <th>Status</th>
              <th aria-label="Actions" />
            </tr>
          </thead>
          <tbody>
            {principals.map((principal) => {
              const state = now === null ? null : principalState(principal, now);
              const working = busy === principal.id;
              return (
                <tr key={principal.id}>
                  <td className="ellip" title={principal.id}>{principal.label}</td>
                  <td className="ellip dim" title={principal.scopes.join(", ")}>{principal.scopes.join(", ")}</td>
                  <td
                    className="dim"
                    title={principal.revoked_at ? `Revoked ${clock.formatDate(principal.revoked_at)}` : `Expires ${clock.formatDate(principal.expires_at)}`}
                  >
                    {state === null ? "—" : (
                      <span className={`state-cell st-${state}`}>
                        {state}
                        {state === "active" && now !== null && (
                          <span className="dim"> · {Math.max(1, Math.ceil((Date.parse(principal.expires_at) - now) / 86_400_000))}d left</span>
                        )}
                      </span>
                    )}
                  </td>
                  <td>
                    {state === "active" && (
                      <div className="row-actions">
                        {confirmingID === principal.id ? (
                          <>
                            <button type="button" className="confirm-danger" disabled={working} onClick={() => void revoke(principal)}>
                              {working ? "Revoking…" : "Confirm"}
                            </button>
                            <button type="button" disabled={working} onClick={() => setConfirmingID(null)}>Cancel</button>
                          </>
                        ) : (
                          <button type="button" className="danger" disabled={busy !== null} onClick={() => setConfirmingID(principal.id)}>
                            Revoke
                          </button>
                        )}
                      </div>
                    )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table></div>
      ) : (
        <p className="empty-note">No principals. The Fleet&rsquo;s service authenticates to <code>/v1/fleets</code> with one.</p>
      )}
      {open ? (
        <form className="inline-form" onSubmit={(event) => void create(event)} onKeyDown={(event) => { if (event.key === "Escape") close(); }}>
          <label className="grow">Label
            <input autoFocus required maxLength={128} value={label} onChange={(event) => setLabel(event.target.value)} placeholder="provisioning-api" />
          </label>
          <label>Expires in (days)
            <input className="narrow" type="number" min={1} max={366} required value={days} onChange={(event) => setDays(event.target.value)} />
          </label>
          <fieldset className="fleet-scopes">
            <legend>Scopes</legend>
            {scopes.map((scope) => (
              <label key={scope} className="check" title={scopeHelp[scope]}>
                <input type="checkbox" checked={chosen.includes(scope)} onChange={(event) => toggle(scope, event.target.checked)} />
                <span className="mono">{scope}</span>
              </label>
            ))}
          </fieldset>
          <button type="submit" disabled={busy !== null || label.trim().length === 0 || chosen.length === 0}>
            {busy === "create" ? "Creating…" : "Create principal"}
          </button>
          <button type="button" className="button quiet" onClick={close} disabled={busy !== null}>Cancel</button>
        </form>
      ) : (
        <div className="add-row">
          <button type="button" onClick={() => { setMessage(null); setOpen(true); }}>
            <span aria-hidden="true">+</span> New principal
          </button>
        </div>
      )}
    </>
  );
}
