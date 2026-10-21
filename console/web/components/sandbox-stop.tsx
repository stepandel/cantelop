"use client";

import { useEffect, useId, useRef, useState } from "react";
import type { Sandbox } from "@/lib/telemetry-types";

type SandboxAction = "stop" | "crash";

export function SandboxStop({ sandbox, onStopped }: { sandbox: Sandbox; onStopped: (sandbox: Sandbox) => void }) {
  const [action, setAction] = useState<SandboxAction | null>(null);
  if (sandbox.state === "terminated") return null;
  return (
    <div className="row-actions" onClick={(event) => event.stopPropagation()} onKeyDown={(event) => event.stopPropagation()}>
      {sandbox.state === "running" && <button type="button" className="danger" aria-label={`Crash ${sandbox.id}`} onClick={() => setAction("crash")}>Crash</button>}
      <button type="button" className="danger" aria-label={`Stop ${sandbox.id}`} onClick={() => setAction("stop")}>Stop</button>
      {action && <SandboxActionDialog action={action} sandbox={sandbox} onChanged={onStopped} onClose={() => setAction(null)} />}
    </div>
  );
}

function SandboxActionDialog({ action, sandbox, onChanged, onClose }: {
  action: SandboxAction;
  sandbox: Sandbox;
  onChanged: (sandbox: Sandbox) => void;
  onClose: () => void;
}) {
  const dialogRef = useRef<HTMLDialogElement>(null);
  const cancelRef = useRef<HTMLButtonElement>(null);
  const inFlight = useRef(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const id = useId();
  const crashing = action === "crash";

  useEffect(() => {
    const dialog = dialogRef.current;
    if (!dialog?.open) dialog?.showModal();
    cancelRef.current?.focus();
  }, []);

  async function submit() {
    if (inFlight.current) return;
    inFlight.current = true;
    setBusy(true);
    setError("");
    try {
      const response = await fetch(`/api/admin/sandboxes/${encodeURIComponent(sandbox.id)}`, { method: crashing ? "POST" : "DELETE" });
      if (!response.ok) {
        const payload = await response.json().catch(() => ({}));
        setError(response.status === 401 ? "Admin access is required. Sign in again to continue."
          : response.status === 404 ? "This sandbox no longer exists. Close this dialog and refresh the list."
          : response.status === 409 ? "The sandbox state changed. Close this dialog and refresh before retrying."
          : payload.error === (crashing ? "sandbox_crash_auth_unavailable" : "sandbox_stop_auth_unavailable") ? "The console cannot authenticate with the sandbox host. The host control credential needs to be updated."
          : crashing ? "Could not confirm the sandbox crashed. Close this dialog and refresh its state before retrying."
          : "Could not confirm the sandbox stopped. Close this dialog and refresh its state before retrying.");
        return;
      }
      onChanged(await response.json() as Sandbox);
      dialogRef.current?.close();
    } catch {
      setError(crashing ? "Could not confirm the sandbox crashed. Check your connection and refresh its state before retrying."
        : "Could not confirm the sandbox stopped. Check your connection and refresh its state before retrying.");
    } finally {
      inFlight.current = false;
      setBusy(false);
    }
  }

  return (
    <dialog ref={dialogRef} className="sandbox-stop-dialog" aria-labelledby={`${id}-title`} aria-describedby={`${id}-description`}
      onCancel={(event) => { if (inFlight.current) event.preventDefault(); }} onClose={onClose}>
      <div className="sandbox-stop-content">
        <h2 id={`${id}-title`}>{crashing ? "Crash sandbox?" : "Stop sandbox?"}</h2>
        <code className="sandbox-stop-id">{sandbox.id}</code>
        <p id={`${id}-description`}>{crashing
          ? "This kills the sandbox process without a graceful shutdown. Use it to test Session recovery after an unexpected sandbox loss."
          : "This will stop the sandbox immediately and interrupt any work running in it."}</p>
        {error && <div className="notice error" role="alert">{error}</div>}
        <div className="sandbox-stop-buttons" aria-busy={busy}>
          <button ref={cancelRef} type="button" className="button" disabled={busy} onClick={() => dialogRef.current?.close()}>Cancel</button>
          <button type="button" className="button danger" disabled={busy} onClick={() => void submit()}>
            {busy ? crashing ? "Crashing…" : "Stopping…" : crashing ? "Crash sandbox" : "Stop sandbox"}
          </button>
        </div>
      </div>
    </dialog>
  );
}
