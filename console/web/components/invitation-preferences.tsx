"use client";
import { useEffect, useState } from "react";
import { SectionHead } from "@/components/section-head";

export function InvitationPreferences() {
  const [token, setToken] = useState<string | null>(null);
  const [status, setStatus] = useState("loading");
  useEffect(() => {
    const value = new URLSearchParams(window.location.hash.slice(1)).get("token");
    window.history.replaceState(null, "", window.location.pathname);
    setToken(value);
    setStatus(value ? "ready" : "invalid");
  }, []);
  async function stop() {
    setStatus("saving");
    try {
      const response = await fetch("/api/invitations/unsubscribe", {
        method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ token }),
      });
      setStatus(response.ok ? "saved" : "error");
    } catch { setStatus("error"); }
  }
  return (
    <>
      <SectionHead title="Invitation emails" subtitle="Stop future app invitations to this address" />
      <div className="section-panel">
        {status === "error" && <div className="notice error" role="alert">Could not save this preference. Try again or contact privacy@cantelop.com.</div>}
        {status === "saved" ? (
          <div className="notice status" role="status">Invitation emails to this address are now stopped. Your account and existing app access are unchanged. Emails already in transit may still arrive.</div>
        ) : (
          <>
            <p className="table-note">Stop future Cantelop app invitations to the address that received this email. This does not affect security messages or existing app access.</p>
            {status === "invalid" ? <p className="table-note">Open the preference link from your invitation email.</p> : (
              <form className="inline-form compact" onSubmit={(event) => { event.preventDefault(); void stop(); }}>
                <button type="submit" disabled={!token || status === "saving"}>{status === "saving" ? "Saving…" : "Stop invitation emails"}</button>
              </form>
            )}
          </>
        )}
        <p className="table-note"><a href="/invitations/privacy">Invitation privacy</a></p>
      </div>
    </>
  );
}
