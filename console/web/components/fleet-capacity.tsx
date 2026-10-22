"use client";

import { useRouter } from "next/navigation";
import { useState, type FormEvent } from "react";

import { capacityLabel, fleetErrorMessage } from "@/lib/fleet-labels";
import { useClock } from "@/lib/use-clock";

/** The Fleet's installation cap and the inline form that changes it. Lowering
 *  the cap below current use blocks new installations; it never deletes. */
export function FleetCapacity({ fleetId, maxApps, usedApps, createdAt }: {
  fleetId: string;
  maxApps: number | null;
  usedApps: number;
  createdAt: string;
}) {
  const router = useRouter();
  const clock = useClock();
  const [open, setOpen] = useState(false);
  const [unlimited, setUnlimited] = useState(maxApps === null);
  const [cap, setCap] = useState(String(maxApps ?? Math.max(usedApps, 100)));
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<{ kind: "error" | "status"; content: string } | null>(null);

  function close() {
    setOpen(false);
    setUnlimited(maxApps === null);
    setCap(String(maxApps ?? Math.max(usedApps, 100)));
  }

  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setBusy(true);
    setMessage(null);
    try {
      const response = await fetch(`/api/customer/fleets/${fleetId}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ max_apps: unlimited ? null : Number(cap) }),
      });
      const body = await response.json().catch(() => ({})) as { max_apps?: number | null; error?: string };
      if (!response.ok) {
        setMessage({ kind: "error", content: fleetErrorMessage(body.error) });
        return;
      }
      setOpen(false);
      setMessage({ kind: "status", content: body.max_apps === null ? "The Fleet now installs without a cap" : `The cap is now ${body.max_apps}` });
      router.refresh();
    } catch {
      setMessage({ kind: "error", content: "The control service is unavailable" });
    } finally {
      setBusy(false);
    }
  }

  const below = !unlimited && Number(cap) < usedApps;
  return (
    <>
      {message && (
        <div className={`notice ${message.kind}`} role={message.kind === "error" ? "alert" : "status"}>{message.content}</div>
      )}
      <dl className="credential-facts">
        <div>
          <dt>Installation cap</dt>
          <dd>
            {open ? (
              <form className="fleet-cap-form" onSubmit={(event) => void save(event)} onKeyDown={(event) => { if (event.key === "Escape") close(); }}>
                <input
                  autoFocus
                  className="control narrow"
                  type="number"
                  aria-label="Installation cap"
                  min={1}
                  max={10000}
                  required={!unlimited}
                  disabled={unlimited}
                  value={unlimited ? "" : cap}
                  onChange={(event) => setCap(event.target.value)}
                  placeholder="∞"
                />
                <label className="check">
                  <input type="checkbox" checked={unlimited} onChange={(event) => setUnlimited(event.target.checked)} />
                  <span>Unlimited</span>
                </label>
                <button type="submit" className="button small" disabled={busy}>{busy ? "Saving…" : "Save"}</button>
                <button type="button" className="button quiet small" onClick={close} disabled={busy}>Cancel</button>
                {below && <span className="warn">Below the {usedApps} in use: new installations stop until usage drops</span>}
              </form>
            ) : (
              <>
                <span>{maxApps === null ? "Unlimited" : maxApps}</span>
                <button type="button" className="button small" onClick={() => { setMessage(null); setOpen(true); }}>Change</button>
              </>
            )}
          </dd>
        </div>
        <div>
          <dt>In use</dt>
          <dd>
            {capacityLabel(usedApps, maxApps)}
            <span className="dim">Apps purging after deletion still count</span>
          </dd>
        </div>
        <div>
          <dt>Account slots</dt>
          <dd>1 <span className="dim">however many Apps the Fleet installs</span></dd>
        </div>
        <div>
          <dt>Created</dt>
          <dd title={clock.formatDate(createdAt)}>{clock.formatStamp(createdAt)}</dd>
        </div>
      </dl>
    </>
  );
}
