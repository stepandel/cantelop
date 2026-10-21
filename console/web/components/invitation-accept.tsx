"use client";

import { useEffect, useState, type FormEvent } from "react";
import { signOut } from "next-auth/react";
import { SectionHead } from "./section-head";
import { invitationAcceptancePath, invitationStorageKey, rememberInvitation } from "@/lib/invitations";

export function InvitationAccept({ email, reauthenticate = false }: { email: string | null; reauthenticate?: boolean }) {
  const [token, setToken] = useState<string | null>(null);
  const [ready, setReady] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [needsSignIn, setNeedsSignIn] = useState(reauthenticate);

  useEffect(() => {
    try {
      setToken(rememberInvitation(window.location.hash, window.sessionStorage));
      window.history.replaceState(null, "", window.location.pathname);
    } catch {
      setError("Please allow browser storage, then reopen the invitation link from your email.");
    }
    setReady(true);
  }, []);

  async function accept(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const response = await fetch("/api/customer/invitations/accept", {
        method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ token }),
      });
      const result = await response.json() as { appID?: string; error?: string };
      if (!response.ok) {
        if (result.error === "reauthentication_required") {
          setNeedsSignIn(true);
          return;
        }
        const errors: Record<string, string> = {
          invalid_invitation: "This invitation has expired, was revoked, or has already been accepted. Ask the app owner for a new invitation.",
          email_mismatch: "Sign in using the email address this invitation was sent to, then try again.",
          unauthorized: "Your session has expired. Sign in again to accept this invitation.",
          user_suspended: "This account cannot accept invitations. Contact the app owner.",
        };
        setError(errors[result.error ?? ""] ?? "Could not accept the invitation. Please try again.");
        return;
      }
      if (!result.appID) throw new Error("missing app");
      window.sessionStorage.removeItem(invitationStorageKey);
      window.location.assign(`/apps/${result.appID}`);
    } catch {
      setError("The control service is unavailable. Please try again.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <SectionHead title="Accept invitation" subtitle="Join an app as an admin on Cantelop" />
      <div className="section-panel">
        {error && <div className="notice error" role="alert">{error}</div>}
        {!ready ? <p className="table-note">Loading invitation…</p> : !token ? (
          <p className="table-note">Open the invitation link from your email to continue.</p>
        ) : email === null ? (
          <>
            <p className="table-note">Sign in or create an account with Google or GitHub using the email address that received this invitation. You’ll return here to accept it.</p>
            <p className="table-note"><a href={`/?callbackUrl=${encodeURIComponent(invitationAcceptancePath)}`}>Sign in to continue →</a></p>
          </>
        ) : needsSignIn ? (
          <>
            <p className="table-note">Signed in as {email}, but your sign-in provider needs to confirm this address again before you can accept the invitation.</p>
            <p className="table-note"><a href={`/?callbackUrl=${encodeURIComponent(invitationAcceptancePath)}`}>Sign in again to continue →</a></p>
          </>
        ) : (
          <>
            <p className="table-note">Signed in as {email}. Accepting gives this account admin access to the app named in your invitation email.</p>
            <form className="inline-form compact" onSubmit={(event) => void accept(event)}>
              <button type="submit" disabled={busy}>{busy ? "Accepting…" : "Accept invitation"}</button>
              <button type="button" className="button quiet" disabled={busy} onClick={() => void signOut({ callbackUrl: invitationAcceptancePath })}>Use another account</button>
            </form>
          </>
        )}
      </div>
    </>
  );
}
