"use client";

import { useEffect, useState } from "react";

import { CopyId } from "@/components/copy-id";
import { formatAgo } from "@/lib/format";
import { backendEnvironment, rotationGraceOptions, type CredentialState } from "@/lib/integration-credentials";
import { useClock } from "@/lib/use-clock";

const rotationLabel = {
  pending: "Rotation is reaching the Edge — the new token works once it is applied",
  applied: "Rotation applied",
  failed: "Rotation did not reach the Edge — retry to resend the new token",
} as const;

/** The App's integration credential: the one secret a customer backend needs
 *  to call the App through @cantelop/sdk. Deployment issues it; this page
 *  reveals and rotates it, the same as `cantelop app credentials [rotate]`. */
export function CredentialsPanel({ appId, initial }: { appId: string; initial: CredentialState | null }) {
  const [state, setState] = useState(initial);
  const [token, setToken] = useState<string | null>(null);
  const [grace, setGrace] = useState<number>(rotationGraceOptions[0].seconds);
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState<"reveal" | "rotate" | null>(null);
  const [message, setMessage] = useState<{ kind: "error" | "status"; content: string } | null>(null);
  const { formatDate } = useClock();

  useEffect(() => {
    if (initial !== null) return;
    let cancelled = false;
    void (async () => {
      try {
        const response = await fetch(`/api/customer/apps/${appId}/integration`, { cache: "no-store" });
        if (!response.ok) throw new Error("unavailable");
        const next = await response.json() as CredentialState;
        if (!cancelled) setState(next);
      } catch {
        if (!cancelled) setMessage({ kind: "error", content: "Credentials are temporarily unavailable. Reload to try again." });
      }
    })();
    return () => { cancelled = true; };
  }, [appId, initial]);

  async function reveal() {
    setBusy("reveal"); setMessage(null);
    try {
      const response = await fetch(`/api/customer/apps/${appId}/integration/reveal`, { method: "POST" });
      if (!response.ok) throw new Error("unavailable");
      setToken((await response.json() as { token: string }).token);
    } catch {
      setMessage({ kind: "error", content: "Could not read the token. Try again." });
    } finally { setBusy(null); }
  }

  async function rotate(body: { grace_seconds: number } | { retry: true }) {
    setBusy("rotate"); setMessage(null); setConfirming(false);
    try {
      const response = await fetch(`/api/customer/apps/${appId}/integration/rotate`, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify(body),
      });
      if (!response.ok) {
        const code = (await response.json().catch(() => ({})) as { error?: string }).error;
        throw new Error(
          code === "integration_rotation_unsupported" ? "The live release predates token rotation. Redeploy with @cantelop/sdk 1.0, or revoke the old token immediately." :
          code === "integration_rotation_unresolved" ? "The previous rotation has not reached the Edge yet. Wait for it, or retry it." :
          "Could not rotate the token. Try again.",
        );
      }
      const next = await response.json() as CredentialState & { token: string };
      setState(next);
      setToken(next.token);
      setMessage({ kind: "status", content: "New token issued. Update your backend's CANTELOP_INTEGRATION_TOKEN before the old one expires." });
    } catch (error) {
      setMessage({ kind: "error", content: error instanceof Error ? error.message : "Could not rotate the token." });
    } finally { setBusy(null); }
  }

  if (state === null) {
    return <div className="section-panel">{message ? <div className={`notice ${message.kind}`} role="alert">{message.content}</div> : <p className="empty-note">Loading credentials…</p>}</div>;
  }
  if (!state.issued) {
    return (
      <div className="section-panel">
        <p className="empty-note">No credential yet</p>
      </div>
    );
  }

  const rotation = state.rotation;
  const environment = backendEnvironment(state.slug, token ?? "<token>");
  return (
    <div className="section-panel credentials-panel">
      {message && <div className={`notice ${message.kind}`} role={message.kind === "error" ? "alert" : "status"}>{message.content}</div>}
      <dl className="credential-facts">
        <div>
          <dt>Edge URL</dt>
          <dd><CopyId value={state.edge_url} /></dd>
        </div>
        <div>
          <dt>App slug</dt>
          <dd><CopyId value={state.slug} /></dd>
        </div>
        <div>
          <dt>Token</dt>
          <dd>
            {token ? <CopyId value={token} /> : (
              <>
                <span className="credential-mask" aria-label="Hidden token">••••••••••••••••••••••••</span>
                <button type="button" className="button small" disabled={busy !== null} onClick={() => void reveal()}>
                  {busy === "reveal" ? "Reading…" : "Reveal"}
                </button>
              </>
            )}
          </dd>
        </div>
        {rotation && (
          <div>
            <dt>Last rotated</dt>
            <dd>
              <time dateTime={rotation.rotated_at} title={formatDate(rotation.rotated_at)}>{formatAgo(rotation.rotated_at)}</time>
              <span className={rotation.status === "failed" ? "bad" : "dim"}>{rotationLabel[rotation.status]}</span>
            </dd>
          </div>
        )}
        {state.previous_token_expires_at && (
          <div>
            <dt>Previous token</dt>
            <dd>Still accepted until {formatDate(state.previous_token_expires_at)}</dd>
          </div>
        )}
      </dl>

      <section className="credential-section">
        <h2>Backend configuration</h2>
        <pre className="code-well">{environment}</pre>
      </section>

      <section className="credential-section">
        <h2>Rotate token</h2>
        {rotation?.status === "failed" ? (
          <div className="credential-rotate">
            <button type="button" className="button" disabled={busy !== null} onClick={() => void rotate({ retry: true })}>
              {busy === "rotate" ? "Retrying…" : "Retry rotation"}
            </button>
          </div>
        ) : (
          <>
            <form className="credential-rotate" onSubmit={(event) => { event.preventDefault(); setConfirming(true); }}>
              <select className="control" aria-label="Old token grace period" value={grace} disabled={confirming} onChange={(event) => setGrace(Number(event.target.value))}>
                {rotationGraceOptions.map((option) => <option key={option.seconds} value={option.seconds}>{option.label}</option>)}
              </select>
              {confirming ? (
                <>
                  <button type="button" className="button danger" disabled={busy !== null} onClick={() => void rotate({ grace_seconds: grace })}>
                    {busy === "rotate" ? "Rotating…" : "Issue new token"}
                  </button>
                  <button type="button" className="button quiet" disabled={busy !== null} onClick={() => setConfirming(false)}>Cancel</button>
                </>
              ) : (
                <button type="submit" className="button" disabled={busy !== null || rotation?.status === "pending"}>Rotate token</button>
              )}
            </form>
          </>
        )}
      </section>
    </div>
  );
}
