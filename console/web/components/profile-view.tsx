"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { type FormEvent, type KeyboardEvent, type ReactNode, useEffect, useState } from "react";

import { ConsoleShell } from "@/components/console-shell";
import { CopyId } from "@/components/copy-id";
import { ExampleDialog } from "@/components/example-dialog";
import type { CustomerApp } from "@/lib/customer-telemetry";
import { formatAgo, initialsOf } from "@/lib/format";
import { fragmentLinkClick } from "@/lib/fragment";
import { buildShellApps, buildShellWorkspaces } from "@/lib/shell-data";
import type { Workspace, Sandbox } from "@/lib/telemetry-types";
import { useClock } from "@/lib/use-clock";
import { useStreamed } from "@/lib/use-streamed";
import { viewerName, type Viewer } from "@/lib/viewer";

function errorMessage(status: number, body: unknown) {
  const error = typeof body === "object" && body !== null && "error" in body
    ? String((body as { error: unknown }).error)
    : "";
  if (status === 409) return "That app slug is already in use";
  if (status === 429) return "This app would exceed your account quota";
  if (status === 503 || error === "cleanup_pending") return "The app is unrouted, but session cleanup must be retried";
  return "The platform could not complete that request";
}

interface Message {
  kind: "error" | "status";
  content: ReactNode;
}

/** The build serving an app on the home page: its version and when it
 *  went live. `null` means nothing has been deployed yet. */
export interface AppDeployment {
  version: number;
  activated_at?: string;
}

/** What the account is running, as the observer last saw it. Null when it is
 *  unavailable. */
export type AccountRuntime = { instances: Sandbox[]; observedAt: string } | null;

// The account home: who is signed in, what the account is running, and every
// app as a card. This absorbed the old /dashboard apps table — one screen owns
// the app inventory, its creation, and its deletion. Runtime state arrives as
// a promise so the inventory paints without waiting on the observer.
export function ProfileView({
  viewer,
  initialApps,
  maxApps,
  usedApps,
  workspaces,
  runtime: runtimeRead,
  deployments = {},
  unavailable,
}: {
  viewer: Viewer;
  initialApps: CustomerApp[];
  maxApps: number | null;
  usedApps: number | null;
  workspaces: Workspace[];
  runtime: Promise<AccountRuntime>;
  /** The serving build per app id; an app with no entry could not be read. */
  deployments?: Record<string, AppDeployment | null>;
  unavailable: boolean;
}) {
  const runtime = useStreamed(runtimeRead);
  const runtimePending = runtime === undefined;
  const instances = runtime?.instances ?? null;
  const observedAt = runtime?.observedAt ?? null;
  const router = useRouter();
  const { formatDate, formatTime } = useClock();
  const [apps, setApps] = useState(initialApps);
  const [usedAppSlots, setUsedAppSlots] = useState(usedApps);
  const [appSlug, setAppSlug] = useState("");
  const [busy, setBusy] = useState<string | null>(null);
  // Messages render next to what caused them: creation results by the form,
  // deletion results by the grid. Errors announce assertively.
  const [formMessage, setFormMessage] = useState<Message | null>(null);
  const [gridMessage, setGridMessage] = useState<Message | null>(null);
  const [confirmingId, setConfirmingId] = useState<string | null>(null);
  // Neither the create form nor the example picker is a standing section:
  // each appears when its link (or the palette) navigates to its fragment —
  // #new-app for the form, #example for the front door's use-case prompts,
  // raised over the screen as a dialog.
  const [panel, setPanel] = useState<"new-app" | "example" | null>(null);

  useEffect(() => {
    const sync = () => {
      const hash = window.location.hash;
      setPanel(hash === "#new-app" ? "new-app" : hash === "#example" ? "example" : null);
    };
    sync();
    window.addEventListener("hashchange", sync);
    return () => window.removeEventListener("hashchange", sync);
  }, []);

  function closePanel() {
    history.replaceState(null, "", window.location.pathname + window.location.search);
    setPanel(null);
    setAppSlug("");
    setFormMessage(null);
  }

  async function createApp(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setBusy("create");
    setFormMessage(null);
    try {
      const response = await fetch("/api/customer/apps", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ app_slug: appSlug }),
      });
      const body = await response.json() as unknown;
      if (!response.ok) {
        setFormMessage({ kind: "error", content: errorMessage(response.status, body) });
        return;
      }
      setApps((current) => [...current, body as CustomerApp].sort((a, b) => a.created_at.localeCompare(b.created_at)));
      setUsedAppSlots((current) => current === null ? null : current + 1);
      closePanel();
      setFormMessage({
        kind: "status",
        content: (
          <>
            App created as <CopyId value={(body as CustomerApp).id} /> — name it in code with{" "}
            <code>cantelop.app({"{"} name: &quot;{(body as CustomerApp).slug}&quot;, … {"}"})</code>,
            then deploy with <code>cantelop deploy</code> ·{" "}
            <Link href="/docs/quick-start">Quick start</Link>
          </>
        ),
      });
    } catch {
      setFormMessage({ kind: "error", content: "The control service is unavailable" });
    } finally {
      setBusy(null);
    }
  }

  async function deleteApp(app: CustomerApp) {
    setBusy(app.id);
    setGridMessage(null);
    try {
      const response = await fetch(`/api/customer/apps/${app.id}`, { method: "DELETE" });
      const body = response.status === 202 || response.status === 204 ? null : await response.json() as unknown;
      if (!response.ok) {
        setGridMessage({ kind: "error", content: errorMessage(response.status, body) });
        return;
      }
      setConfirmingId(null);
      setGridMessage({ kind: "status", content: "App archive requested. It will disappear after API and Session admission close; workspace data remains retained for operator-controlled recovery." });
    } catch {
      setGridMessage({ kind: "error", content: "The control service is unavailable" });
    } finally {
      setBusy(null);
    }
  }

  const live = (instances ?? []).filter(({ state }) => state === "running");
  const totalMemoryMiB = live.reduce((total, instance) => total + instance.memory_mib, 0);

  const shellApps = buildShellApps(apps, workspaces, instances);
  const toneByAppId = new Map(shellApps.map((app) => [app.id, app.tone ?? "idle"]));

  // Ownership decides which Apps can be deleted. Quota usage comes from the
  // server because an entire Fleet, including its installations, uses one slot.
  const ownedApps = apps.filter((app) => app.role === "owner");
  const sharedApps = apps.filter((app) => app.role !== "owner");
  const atQuota = maxApps !== null && usedAppSlots !== null && usedAppSlots >= maxApps;

  function renderCard(app: CustomerApp) {
    const inst = live.filter((instance) => instance.app_id === app.id).length;
    const deployment = deployments[app.id];
    const tone = toneByAppId.get(app.id) ?? "idle";
    const open = () => router.push(`/apps/${app.id}`);
    return (
      <article
        key={app.id}
        className="app-card"
        role="link"
        tabIndex={0}
        aria-label={`Open ${app.slug}`}
        onClick={open}
        onKeyDown={(event: KeyboardEvent<HTMLElement>) => {
          if (event.key !== "Enter" && event.key !== " ") return;
          event.preventDefault();
          open();
        }}
      >
        <header className="app-card-head">
          <h3 title={app.id}>
            {app.slug}
            {/* Owner is the default reading of a card; only the exception is marked. */}
            {app.role !== "owner" && <span className="role-tag">{app.role}</span>}
          </h3>
          <span className={`state-cell st-${tone === "degraded" ? "failed" : tone}`}>
            <span className={`dot dot-${tone}`} aria-hidden="true" />
            {runtimePending ? "—" : tone}
          </span>
        </header>
        <dl className="monitor-meta">
          <dt>Sandboxes</dt>
          <dd>{instances === null ? "—" : inst}</dd>
          {/* The serving build and its age: what an idle card can still
              say. No entry means the history could not be read, and the
              card says nothing rather than something wrong. */}
          {app.id in deployments && (
            <>
              <dt className="sep" aria-hidden="true">·</dt>
              {deployment ? (
                <>
                  <dt>Build</dt>
                  <dd>v{deployment.version}</dd>
                  <dd className="faint" title={deployment.activated_at ? formatDate(deployment.activated_at) : undefined}>
                    {formatAgo(deployment.activated_at)}
                  </dd>
                </>
              ) : (
                <dd className="faint">Never deployed</dd>
              )}
            </>
          )}
        </dl>
        <footer className="app-card-foot">
          {/* Who else is on the app. An owner sees how many admins they have
              added; an admin sees whose app it is. */}
          <dl className="monitor-meta people">
            {app.role === "owner" ? (
              app.admin_count > 0 ? (
                <>
                  <dt>Admins</dt>
                  <dd>{app.admin_count}</dd>
                </>
              ) : (
                <dd className="faint">Only you</dd>
              )
            ) : (
              <>
                <dt>Owner</dt>
                <dd className="ellip" title={app.owner?.email}>{app.owner?.email ?? "—"}</dd>
              </>
            )}
          </dl>
          {app.role === "owner" && (
            <div
              className="row-actions"
              onClick={(event) => event.stopPropagation()}
              onKeyDown={(event) => event.stopPropagation()}
            >
              {confirmingId === app.id ? (
                <>
                  <button type="button" className="confirm-danger" disabled={busy === app.id} onClick={() => void deleteApp(app)}>
                    {busy === app.id ? "Deleting…" : "Confirm"}
                  </button>
                  <button type="button" disabled={busy === app.id} onClick={() => setConfirmingId(null)}>
                    Cancel
                  </button>
                </>
              ) : (
                <button type="button" className="danger" disabled={busy === app.id} onClick={() => setConfirmingId(app.id)}>
                  Delete
                </button>
              )}
            </div>
          )}
        </footer>
      </article>
    );
  }

  return (
    <ConsoleShell
      viewer={viewer}
      apps={shellApps}
      maxApps={maxApps}
      usedApps={usedAppSlots}
      workspaces={buildShellWorkspaces(apps, workspaces)}
      activeNav="profile"
      crumbs={[{ label: "Home" }]}
    >
      <div className="screen-head centered">
        <div className="head-identity">
          {viewer.avatarUrl
            ? <img className="profile-avatar" src={viewer.avatarUrl} alt="" width={26} height={26} />
            : <span className="profile-avatar" aria-hidden="true">{initialsOf(viewer.email ?? viewer.login)}</span>}
          <div className="head-id tight">
            <h1>{viewerName(viewer)}</h1>
            {viewer.email && <span>{viewer.email}</span>}
          </div>
        </div>
        {/* The quota counts standalone Apps and Fleets. Shared Apps and Fleet
            installations consume no additional slots. */}
        <div className="stat">
          <span>Apps</span>
          <strong className={atQuota ? "warn" : undefined}>
            {usedAppSlots ?? "—"} / {maxApps ?? "—"}
            {sharedApps.length > 0 && <small className="stat-note">+{sharedApps.length} shared</small>}
          </strong>
        </div>
        {(runtimePending || instances !== null) && (
          <>
            <div className="stat">
              <span>Sandboxes</span>
              <strong className={live.length === 0 ? "dim" : undefined}>{runtimePending ? "—" : live.length}</strong>
            </div>
            <div className="stat">
              <span>Memory allocated</span>
              <strong className={totalMemoryMiB === 0 ? "dim" : undefined}>
                {runtimePending ? "—" : totalMemoryMiB >= 1024 ? `${(totalMemoryMiB / 1024).toFixed(1)} gib` : `${totalMemoryMiB} mib`}
              </strong>
            </div>
          </>
        )}
        <div className="stat">
          <span>Observed</span>
          <strong className="quiet" title={observedAt ? formatDate(observedAt) : undefined}>
            {observedAt ? formatTime(observedAt) : "—"}
          </strong>
        </div>
      </div>

      {unavailable && <div className="notice error">App records are temporarily unavailable</div>}
      {!unavailable && !runtimePending && instances === null && (
        <div className="notice error">Runtime state is temporarily unavailable — showing account records only</div>
      )}
      {gridMessage && (
        <div className={`notice ${gridMessage.kind}`} role={gridMessage.kind === "error" ? "alert" : "status"}>
          {gridMessage.content}
        </div>
      )}

      {/* The group label appears only once there is a second group to tell
          apart: an account that owns everything it sees reads as before. */}
      <section className="app-group" aria-labelledby={sharedApps.length > 0 ? "owned-apps" : undefined}>
        {sharedApps.length > 0 && <h2 id="owned-apps" className="app-group-label">Your apps</h2>}
        <div className="app-grid">
          {ownedApps.map(renderCard)}
          {/* The create tile closes the owned group because that is the group
              the quota governs; at the limit it says so instead of opening a
              form that would only answer 429. */}
          {atQuota ? (
            <div className="app-card app-card-new quota">
              <span>App quota reached</span>
            </div>
          ) : (
            <Link className="app-card app-card-new" href="/profile#new-app" onClick={fragmentLinkClick}>
              <i aria-hidden="true">+</i>
              <span>New app</span>
            </Link>
          )}
          {/* The second way in, for an account with nothing in it yet: the
              front door's use-case prompts, which a coding agent turns into a
              project that deploys here. Once there is an app, the tile goes. */}
          {apps.length === 0 && !unavailable && (
            <Link className="app-card app-card-new" href="/profile#example" onClick={fragmentLinkClick}>
              <i aria-hidden="true">›</i>
              <span>Start from an example</span>
            </Link>
          )}
        </div>
      </section>

      {sharedApps.length > 0 && (
        <section className="app-group" aria-labelledby="shared-apps">
          <h2 id="shared-apps" className="app-group-label">Shared with you</h2>
          <div className="app-grid">
            {sharedApps.map(renderCard)}
          </div>
        </section>
      )}

      {apps.length === 0 && !unavailable && panel !== "new-app" && (
        <p className="empty-note">
          No apps yet · <Link href="/docs/quick-start">Quick start</Link>
        </p>
      )}

      {formMessage && (
        <div className={`notice ${formMessage.kind}`} role={formMessage.kind === "error" ? "alert" : "status"}>
          {formMessage.content}
        </div>
      )}
      {panel === "example" && <ExampleDialog onClose={closePanel} />}
      {panel === "new-app" && (
        <form
          className="inline-form compact"
          id="new-app"
          onSubmit={createApp}
          onKeyDown={(event) => { if (event.key === "Escape") closePanel(); }}
        >
          <input
            autoFocus
            required
            aria-label="App slug"
            pattern="(?!.*--)[a-z0-9](?:[a-z0-9\-]{0,49}[a-z0-9])?"
            maxLength={51}
            value={appSlug}
            onChange={(event) => setAppSlug(event.target.value.toLowerCase())}
            placeholder="App slug"
          />
          <button type="submit" disabled={busy === "create" || appSlug.length === 0}>{busy === "create" ? "Creating…" : "Create"}</button>
          <button type="button" className="button quiet" onClick={closePanel} disabled={busy === "create"}>Cancel</button>
          <span className="dim">Reserves the slug your code names in <code>cantelop.app({"{"} name {"}"})</code> — <code>cantelop deploy --create-app</code> does the same</span>
        </form>
      )}
    </ConsoleShell>
  );
}
