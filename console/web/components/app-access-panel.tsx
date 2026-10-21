"use client";

import { useRouter } from "next/navigation";
import { useEffect, useState, type FormEvent } from "react";
import { useClock } from "@/lib/use-clock";
import { invitationStatus, type Invitation } from "@/lib/invitations";

interface Person { userID: string; email: string }
interface Collaborator extends Person { role: "admin"; createdAt: Date | string }

function addErrorMessage(code?: string) {
  if (code === "not_found") return "This app or invitation is no longer available";
  if (code === "already_invited") return "An invitation is already pending — resend it from its row";
  if (code === "invite_rate_limited") return "Too many invitations just now — wait before sending again";
  if (code === "invitation_suppressed") return "Invitation emails to this address are stopped — reach the recipient another way";
  if (code === "conflict") return "That account is already an admin or cannot be added";
  return "Could not add this admin";
}

function isoString(value: Date | string) {
  return typeof value === "string" ? value : value.toISOString();
}

/** The people who can manage one App, as one table: the owner, each admin
 *  the owner has added, and each person the owner has invited but who has
 *  not accepted yet. The Role column says what a person is on the app; the
 *  Status column says where they are — added on a date, invited and waiting,
 *  or an invitation that expired or failed to send. The owner adds or
 *  invites through the collapsed inline form under the table; every other
 *  action lives on its row. */
export function AppAccessPanel({ appId, owner, initialCollaborators, initialInvitations = [], canManage }: {
  appId: string;
  owner: Person;
  initialCollaborators: Collaborator[];
  initialInvitations?: Invitation[];
  canManage: boolean;
}) {
  const router = useRouter();
  const clock = useClock();
  const [invitations, setInvitations] = useState(initialInvitations);
  useEffect(() => { setInvitations(initialInvitations); }, [initialInvitations]);
  const [now, setNow] = useState<number | null>(null);
  useEffect(() => {
    setNow(Date.now());
    const timer = setInterval(() => setNow(Date.now()), 30_000);
    return () => clearInterval(timer);
  }, []);
  const [collaborators, setCollaborators] = useState(initialCollaborators);
  const [open, setOpen] = useState(false);
  const [email, setEmail] = useState("");
  const [busy, setBusy] = useState<string | null>(null);
  useEffect(() => {
    if (!canManage || busy !== null || invitations.length === 0) return;
    const timer = setInterval(() => router.refresh(), 30_000);
    return () => clearInterval(timer);
  }, [canManage, busy, invitations.length, router]);
  const [confirmingID, setConfirmingID] = useState<string | null>(null);
  const [message, setMessage] = useState<{ kind: "error" | "status"; content: string } | null>(null);

  function close() {
    setOpen(false);
    setEmail("");
    setMessage(null);
  }

  async function add(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setBusy("add");
    setMessage(null);
    try {
      const response = await fetch(`/api/customer/apps/${appId}/collaborators`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ email }),
      });
      if (!response.ok) {
        const body = await response.json().catch(() => ({})) as { error?: string };
        setMessage({ kind: "error", content: addErrorMessage(body.error) });
        return;
      }
      const result = await response.json() as { items?: Collaborator[]; invitation?: Invitation };
      if (result.invitation) {
        setInvitations((current) => [...current, result.invitation!]);
        deliveryMessage(result.invitation);
      } else if (result.items) {
        setCollaborators((current) => [...current, ...result.items!]);
        router.refresh();
        setInvitations((current) => current.filter((item) => item.email !== result.items![0]?.email));
        setMessage({ kind: "status", content: `${email} added as admin` });
      }
      setEmail("");
      setOpen(false);
    } catch {
      setMessage({ kind: "error", content: "The control service is unavailable" });
    } finally {
      setBusy(null);
    }
  }

  function deliveryMessage(invitation: Invitation) {
    setMessage(["sent", "delivered", "delayed"].includes(invitation.delivery)
      ? { kind: "status", content: `Invitation sent to ${invitation.email} — delivery not yet confirmed` }
      : { kind: "error", content: `The invitation email to ${invitation.email} could not be sent — resend it from its row` });
  }

  async function updateInvitation(invitation: Invitation, action: "resend" | "revoke") {
    setBusy(invitation.id);
    setMessage(null);
    try {
      const response = await fetch(`/api/customer/apps/${appId}/invitations/${invitation.id}`, {
        method: action === "resend" ? "POST" : "DELETE",
      });
      const body = await response.json() as { invitation?: Invitation; error?: string };
      if (!response.ok) {
        setMessage({ kind: "error", content: addErrorMessage(body.error) });
        return;
      }
      setInvitations((current) => current.flatMap((item) => item.id !== invitation.id ? [item] : body.invitation ? [body.invitation] : []));
      if (body.invitation) deliveryMessage(body.invitation);
      else setMessage({ kind: "status", content: `Invitation to ${invitation.email} revoked` });
    } catch {
      setMessage({ kind: "error", content: "The control service is unavailable" });
    } finally {
      setBusy(null);
    }
  }

  async function remove(person: Collaborator) {
    setBusy(person.userID);
    setMessage(null);
    try {
      const response = await fetch(`/api/customer/apps/${appId}/collaborators/${person.userID}`, { method: "DELETE" });
      if (!response.ok) {
        setMessage({ kind: "error", content: "Could not remove this admin" });
        return;
      }
      setCollaborators((current) => current.filter((item) => item.userID !== person.userID));
      router.refresh();
      setMessage({ kind: "status", content: `${person.email} removed` });
    } catch {
      setMessage({ kind: "error", content: "The control service is unavailable" });
    } finally {
      setBusy(null);
      setConfirmingID(null);
    }
  }

  // An invitation's status, with the deadline while it is still open: the
  // one fact a waiting row has to offer. A row that can no longer be
  // accepted — expired, bounced, stopped — shows only why.
  function invitationCell(invitation: Invitation) {
    const status = invitationStatus(invitation, now);
    const expires = isoString(invitation.expiresAt);
    const waiting = ["sent", "delivered", "delayed"].includes(invitation.delivery) && status !== "Expired";
    if (!waiting || now === null) return <td className="dim">{status}</td>;
    // Days rather than a stamp: the row is about whether there is still
    // time, and the exact moment sits in the title.
    const daysLeft = Math.max(1, Math.ceil((new Date(expires).getTime() - now) / 86_400_000));
    return (
      <td className="dim" title={`Expires ${clock.formatDate(expires)}`}>
        {status} · {daysLeft}d left
      </td>
    );
  }

  return (
    <>
      {message && (
        <div className={`notice ${message.kind}`} role={message.kind === "error" ? "alert" : "status"}>
          {message.content}
        </div>
      )}
      <div className="access-table-wrap"><table className="data-table access-table">
        <colgroup>
          <col />
          <col className="c-kind" />
          <col className="c-status" />
          {canManage && <col className="c-actions" />}
        </colgroup>
        <thead>
          <tr>
            <th>Member</th>
            <th>Role</th>
            <th>Status</th>
            {canManage && <th aria-label="Actions" />}
          </tr>
        </thead>
        <tbody>
          <tr>
            <td className="ellip" title={owner.email}>{owner.email}</td>
            <td className="kind">Owner</td>
            <td className="faint">—</td>
            {canManage && <td />}
          </tr>
          {collaborators.map((person) => {
            const working = busy === person.userID;
            const added = isoString(person.createdAt);
            return (
              <tr key={person.userID}>
                <td className="ellip" title={person.email}>{person.email}</td>
                <td className="kind">Admin</td>
                <td className="dim" title={clock.formatDate(added)}>Added {clock.formatStamp(added)}</td>
                {canManage && (
                  <td>
                    <div className="row-actions">
                      {confirmingID === person.userID ? (
                        <>
                          <button type="button" className="confirm-danger" disabled={working} onClick={() => void remove(person)}>
                            {working ? "Removing…" : "Confirm"}
                          </button>
                          <button type="button" disabled={working} onClick={() => setConfirmingID(null)}>
                            Cancel
                          </button>
                        </>
                      ) : (
                        <button type="button" className="danger" disabled={busy !== null} onClick={() => setConfirmingID(person.userID)}>
                          Remove
                        </button>
                      )}
                    </div>
                  </td>
                )}
              </tr>
            );
          })}
          {canManage && invitations.map((invitation) => (
            <tr key={invitation.id}>
              <td className="ellip" title={invitation.email}>{invitation.email}</td>
              <td className="kind">Admin</td>
              {invitationCell(invitation)}
              <td><div className="row-actions">
                <button
                  type="button"
                  // An address that bounced, complained, or opted out gets no further mail.
                  disabled={busy !== null || ["suppressed", "bounced", "complained"].includes(invitation.delivery)}
                  title="Sends a fresh link; the previous one stops working"
                  onClick={() => void updateInvitation(invitation, "resend")}
                >
                  Resend
                </button>
                <button type="button" className="danger" disabled={busy !== null} onClick={() => void updateInvitation(invitation, "revoke")}>Revoke</button>
              </div></td>
            </tr>
          ))}
        </tbody>
      </table></div>
      {canManage ? (
        open ? (
          <form className="inline-form compact" onSubmit={(event) => void add(event)} onKeyDown={(event) => { if (event.key === "Escape") close(); }}>
            <input
              autoFocus
              required
              type="email"
              aria-label="Email of the admin to add or invite"
              value={email}
              onChange={(event) => setEmail(event.target.value)}
              placeholder="person@example.com"
            />
            <button type="submit" disabled={busy !== null || email.length === 0}>{busy === "add" ? "Adding…" : "Add admin"}</button>
            <button type="button" className="button quiet" onClick={close} disabled={busy !== null}>Cancel</button>
          </form>
        ) : (
          <div className="add-row">
            <button type="button" onClick={() => setOpen(true)} aria-label="Add admin">
              <span aria-hidden="true">+</span> Add admin
            </button>
          </div>
        )
      ) : (
        <p className="table-note">Only the owner can change access.</p>
      )}
    </>
  );
}
