# Application domain model

This document records the target control-plane model and the boundary between
customer resources and the Firecracker implementation. There is no legacy data
migration contract before the first production deployment.

## Resource hierarchy

```text
User ── owns many ── App
                    ├── Admin collaborators
    ├── Builds
    └── Workspaces
        └── Sessions
            └── Sandbox (internal implementation)
```

An **App** owns its globally routable slug, immutable Builds, Session runtime,
shared capacity, and runtime policy. Cantelop assigns the Session runtime's private
listener port and injects it into the SDK runtime. A newly created App has no
active Build until its first release succeeds.

Every App has exactly one owner and may have multiple admin collaborators. The
owner is the billing and quota principal. Both roles can read and operate the
App, including deployments, configuration, and Workspaces; only the owner can
manage collaborators or archive the App. Membership is checked on every
request, so removing an admin revokes access without waiting for login expiry.

A **Workspace** is the durable, addressable boundary within an App. It owns a
stable `wsp_…` ID, an App-scoped slug, and persistent application data. The App
is public at `<app-slug>.cantelop.dev`; its API selects a Workspace explicitly
through the SDK. Creation is immediate:
the authoritative record is returned in the creating transaction. Any queued
provider registration is an asynchronous projection, not Workspace lifecycle
state. A Workspace has no provisioning state machine; `archived_at` is its only
lifecycle marker.

A **Session** is the public runtime identity for one Workspace. It has a
stable `ses_…` ID and may be selected and reused. Its public state is `active`
while it has a Sandbox and `idle` otherwise; neither state is terminal.
Releasing an active Session terminates only its current Sandbox. A later
activation may allocate a replacement behind the same Session ID and resolves
the Workspace's current active Build, so a Session does not pin a Build.
Runtime may also bind a hashed, Workspace-scoped application key to one Session
for distributed get-or-create operations. That binding is an internal lookup
mechanism: Cantelop still generates the Session ID, and the key is not part of
the public Session resource.

A **Sandbox** is the internal isolated microVM that implements a Session.
Firecracker, jailer, networking, host-resource ownership, and crash recovery may
use Sandbox terminology. Sandbox identity and provider details are not part of
the public resource model.

## Runtime policy

Host VM, guest-memory, and startup limits bound concurrent Sandbox allocation.
Guest vCPUs do not reserve host cores; equal cgroup weights share host CPU
subject to a four-core per-Sandbox ceiling.
Sessions and Workspaces do not reserve compute capacity when they are created.

Every request targets an explicit Session. The Session's `keep_alive_seconds`
is the sole Sandbox idle setting and accepts 0 through 604,800 seconds. It
begins after the Session runtime becomes quiescent. Expiry releases the Sandbox
without terminating the Session; the next request reactivates it.

## Routing and activation

```text
App hostname
  -> customer API Worker
  -> explicit Workspace selection
  -> App Session runtime + active Build
  -> selected or newly created Session
  -> internal Sandbox
```

Workspace and Session are the only public names at these two boundaries.
Provider runtimes and Sandboxes remain implementation abstractions below them.

## Migration contract

This release is a clean protocol cutoff. The control plane applies its one-way
PostgreSQL Session runtime migration under an advisory lock. Fire Fuse accepts
only its current schema and rejects every older or structurally modified SQLite
database. There are no compatibility aliases or multi-version runtime paths.
