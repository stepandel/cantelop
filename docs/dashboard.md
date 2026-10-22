# Cantelop App dashboard

Cantelop's Next.js stack is the external product control plane. `/profile`
(the account home) and the `/apps/{id}` App screens are customer views scoped
to the opaque user ID in the signed session;
`/admin` is the operator's global host view. PostgreSQL is authoritative for
users, ownership, quotas, Apps, Session runtimes, Workspaces, and durable
operations. Browser handlers call the local product service instead of a
public Fire Fuse endpoint; the operator view still reads the host observer.

## Naming boundary

Cantelop is the product name presented by the dashboard. The underlying
orchestrator retains the internal `fire-fuse` name, including its binaries,
services, filesystem paths, observer, and `FIRE_FUSE_*` environment variables.
Keeping that boundary avoids an infrastructure migration and does not expose
the orchestrator directly to dashboard users.

## Security boundary

`fire-fuse` remains bound to loopback and requires its private control bearer
token. Never route its `/v1` API through the public proxy.
`fire-fuse-observer` is the only product component that reads it and exposes
bearer-authenticated, read-only endpoints:
`GET /snapshot` for current Workspace, derived Session allocation state, and
Sandbox lifecycle state, and
`GET /requests` for bounded
request history. `GET /infrastructure` returns only the
curated Fire Fuse charts read from the loopback-only Netdata API, with fixed
one-, six-, or twenty-four-hour windows and bounded point counts. It is not a
generic Netdata proxy. `GET /sandboxes/{id}/database-access` returns only one
active Sandbox's current App/Workspace assignment for database authorization,
without collecting dashboard inventories or usage. It uses the same read
credential and returns 404 for missing or inactive Sandboxes. The observer also
exposes narrowly scoped mutations,
`DELETE /sessions/{id}`, `DELETE /sandboxes/{id}`, and
`POST /sandboxes/{id}/crash`, under a distinct bearer credential. Sandbox
stopping and crash injection are available only in the admin console, with
server-side admin authorization and same-origin checks. The observer
binds to loopback so nginx remains responsible for public TLS.

Generate two independent random credentials on the deployment controller:

```sh
openssl rand -hex 32
```

Store them as `vault_fire_fuse_observer_token` and
`vault_fire_fuse_session_mutation_token` in the encrypted Ansible Vault for
the `fire_fuse_hosts` group. The observer role installs them as private files
owned by the observer service account. For a manual recovery installation, use:

```sh
sudo install -d -o root -g fire-fuse-observer -m 0750 /etc/fire-fuse-observer
sudo install -o root -g fire-fuse-observer -m 0640 observer.token \
  /etc/fire-fuse-observer/observer.token
sudo install -o root -g fire-fuse-observer -m 0640 session-mutation.token \
  /etc/fire-fuse-observer/session-mutation.token
```

Build `fire-fuse-observer-linux-amd64` with the normal Linux build script, then
deploy the checksum-pinned artifact through the observer playbook:

```sh
ansible-playbook \
  --inventory platform/ansible/inventory/staging/hosts.yml \
  --ask-vault-pass \
  platform/ansible/playbooks/compute/observer.yml \
  --extra-vars '{
    "fire_fuse_observer_binary_artifact":"/absolute/controller/path/dist/fire-fuse-observer-linux-amd64",
    "fire_fuse_observer_binary_checksum":"<sha256>"
  }'
```

The role owns the binary, systemd unit, token permissions, exact nginx site,
service activation, and authenticated loopback and public-TLS qualification.
The observer reads current state from `127.0.0.1:8080` and listens on
`127.0.0.1:8082`.

Give the observer its own exact hostname. The existing wildcard certificate
for `*.apps.cantelop.dev` can cover a name such as
`status.apps.cantelop.dev`. Configure this exact nginx server independently
from the Workspace ingress:

```nginx
server {
    listen 443 ssl;
    listen [::]:443 ssl;
    server_name status.apps.cantelop.dev;

    ssl_certificate /etc/letsencrypt/live/apps.cantelop.dev/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/apps.cantelop.dev/privkey.pem;

    location = /snapshot {
        proxy_pass http://127.0.0.1:8082/snapshot;
        proxy_set_header Authorization $http_authorization;
        proxy_set_header Host $host;
        proxy_pass_request_body off;
    }

    location = /requests {
        proxy_pass http://127.0.0.1:8082/requests;
        proxy_set_header Authorization $http_authorization;
        proxy_set_header Host $host;
        proxy_pass_request_body off;
    }

    location = /infrastructure {
        proxy_pass http://127.0.0.1:8082/infrastructure;
        proxy_set_header Authorization $http_authorization;
        proxy_set_header Host $host;
        proxy_pass_request_body off;
    }

    location ~ "^/sandboxes/sbx-[0-9a-f]{32}/database-access$" {
        limit_except GET { deny all; }
        proxy_pass http://127.0.0.1:8082$request_uri;
        proxy_set_header Authorization $http_authorization;
        proxy_set_header Host $host;
        proxy_pass_request_body off;
    }

    location ~ "^/sessions/ses_[A-Za-z0-9][A-Za-z0-9_-]{0,59}$" {
        limit_except DELETE { deny all; }
        proxy_pass http://127.0.0.1:8082$request_uri;
        proxy_set_header Authorization $http_authorization;
        proxy_set_header Host $host;
        proxy_pass_request_body off;
    }

    location ~ "^/sandboxes/sbx-[0-9a-f]{32}$" {
        limit_except DELETE { deny all; }
        proxy_pass http://127.0.0.1:8082$request_uri;
        proxy_set_header Authorization $http_authorization;
        proxy_set_header Host $host;
        proxy_pass_request_body off;
    }

    location ~ "^/sandboxes/sbx-[0-9a-f]{32}/crash$" {
        limit_except POST { deny all; }
        proxy_pass http://127.0.0.1:8082$request_uri;
        proxy_set_header Authorization $http_authorization;
        proxy_set_header Host $host;
        proxy_pass_request_body off;
    }

    location / {
        return 404;
    }
}
```

The bearer tokens must be stored only in private host files and the Vercel
project's encrypted environment variables. They must never be added
to browser code or a `NEXT_PUBLIC_` variable.

## Runtime inventory

The observer composes Apps, Workspaces, Sessions, Sandboxes, and current warm
slot capacity from the canonical loopback endpoints. Sessions remain public runtime identities with
their lifecycle, keep-alive policy, Build, and timestamps. Sandboxes are a
separate host resource inventory with shape, startup timing, network identity,
and current CPU and memory readings. The sandbox Memory column and graph use
`guest_memory_used_bytes`: guest `MemTotal - MemAvailable`, excluding available
memory and reclaimable cache. This includes guest OS and workload consumption,
and can fall before the platform removes allocated guest memory. The guest is
sampled every two seconds; missing or more than ten-second-old observations are
unavailable, never replaced with host consumption.

`memory_current_bytes` remains the physical host cgroup charge for infrastructure
accounting, and `guest_memory_mib` remains allocated guest capacity. Neither is
used as sandbox consumption in the console. Capacity contains only aggregate
target, ready, claimed, and preparing counts; host-only recovery metadata and
checkpoint details are never emitted.

When the observer's managed runtime is configured, `/snapshot` combines the
standard and isolated Fleet pools, including Workspace-scoped reads, live
Sessions, Sandbox usage, and warm capacity. Both pools must be readable; an
unavailable pool fails the snapshot instead of appearing empty. Readiness
requires both pools to be ready. Duplicate runtime identities fail the snapshot.
Session stop, Sandbox stop, crash injection, `/requests`, and
`/apps/{id}/builds` resolve the owning pool first: Sandboxes and Apps by a
direct read, Sessions from the Session listings, and request history by the
pool that has events for the scoped App or Workspace. An owner found in one
pool is used even when the other pool cannot be read; without an owner, a
failed lookup is an error and never falls through to the other pool, and an
ambiguous identity is a conflict. Database authorization keeps its separate
standard and managed routes and credentials.

## Infrastructure metrics

The authenticated `GET /infrastructure` endpoint accepts only `range=1h`,
`range=6h`, or `range=24h` and defaults to one hour. It reports each of the
three machines — `compute`, `storage`, and `builder` — as a host with a fixed
chart set: CPU, memory, and root filesystem for every machine, plus the
`fire_fuse.*` service charts that machine owns. Compute's Netdata is the
parent; the storage and builder agents stream to it over their WireGuard
links, so the observer reads every machine through `127.0.0.1:19999` (children
under `/host/storage` and `/host/builder`) and never needs a route to them.
It caps every upstream response at one MiB and returns no Netdata
configuration, host identity, credentials, arbitrary chart selection, or
unbounded query controls.

A host is `reachable` while its newest `system.cpu` sample is under 60 seconds
old. A child that has stopped streaming keeps its history and its
`last_collected_at`; one that has never connected has neither. Only a failure
of the compute parent fails the endpoint. The top-level `charts` field repeats
the compute host's charts for consoles that predate per-host metrics.
Console renders a card per machine — reachability and current service health,
never a stale reading as current — above that machine's graphs.

## Request history

For every edge request dispatched to a Workspace, the gateway records one event
after the response completes. The event timestamp is captured
at ingress and its duration includes activation plus proxy time. Successful
responses, upstream failures, and activation failures are retained; an event
may omit `session_id` when routing failed before activation completed.

The write goes through fire-fuse's loopback API into the shared SQLite state
database. It has a two-second deadline and is idempotent by event ID. A storage
failure is logged but never replaces or changes the App response.

The observer exposes bounded, newest-first history at authenticated
`GET /requests`, accepting `workspace_id` or `app_id`, `limit`, and
`before_sequence` or `after_sequence`. nginx
publishes this endpoint alongside `/snapshot`; the fire-fuse
`/v1` API remains private to the host.

The operator `/admin` home shows the host header and **Infrastructure**
graphs, with separate **Sandboxes** (actual microVM allocations and live
resource use), **Traces**, and **Workspaces** screens. Workspaces lists every
live Workspace and unfinished deletion with its managed database state, and
can delete a Workspace, retry a failed purge, or check Turso for database drift
(see [Managed Workspace databases](workspace-databases.md#console-surfaces)). Infrastructure renders the curated
host utilization, health, storage latency, capacity, cache, and error series
through the observer's bounded endpoint; the Netdata UI and API remain on host
loopback. Its host summary shows active Sandboxes beside immediately claimable
warm slots, while the shared time-range control also drives the host-wide
Sandbox startup-duration graph.
The same view retains the recent pre-Workspace edge failure table. The customer App-scoped
observability surface is described below.

## Customer Apps

`/profile` lists only Apps owned by the signed-in user as cards and owns App
creation; `/dashboard` and `/apps` both redirect there (old
`/dashboard#new-app` links keep their fragment). The creation form accepts
only a DNS slug. Cantelop assigns the private workload port and uses the
platform's default 4-vCPU Sandbox profile with 512 MiB boot memory and automatic growth up to 4096 MiB of hotplug memory, with
zero prewarmed instances. Extra fields, including workload ports and Build IDs,
are rejected rather than passed through to the control plane. The page shows
the generated App ID. Unified API and Session runtime deployment is available through
the Cantelop CLI. The CLI uploads both artifacts directly to private object
storage and finalizes one release; image archives never pass through an
ordinary console request body.

Deletion requires a same-origin request. It atomically archives the owned App,
its Workspaces in PostgreSQL, immediately removing them
from product reads and quota reservation. A durable provider operation then
converges Fire Fuse to the desired absence and queues a purge for every
Workspace, permanently deleting its files and managed database.

### The App screen

Everything an App owns opens under `/apps/{id}`: **Workspaces**
(`/workspaces`: the App's workspace records with each managed database's
provisioning state, and the form that creates one),
**Builds** (`/builds`), **Environment** (`/configuration`), **Access**
(`/access`), and **API** (`/api`, with per-route detail at `/api/route`). A
Workspace is a row, not a destination: the console has no per-Workspace route
at all.

The App home, `/apps/{id}` itself, renders the request and runtime graphs;
the old `/apps/{id}/graphs` path redirects there. The **Monitoring** group
holds **Sandboxes** (`/sandboxes`) and **Traces** (`/traces`).

Request history combines both halves of a request. The edge dispatcher mints a
request ID, records the Worker invocation against it — method, path,
application logs — and forwards the same ID to the workspace gateway on
`X-Cantelop-Edge-Request-ID`, which writes it into the durable request log
beside the Workspace and Session that served the call. The graphs take status
and duration from the gateway log wherever it has them, because that log pages
back through the whole history while Worker observability holds only the last
six hours; the edge leg contributes what the gateway cannot see.

## Vercel project

Create a Vercel project for Cantelop with `console` as its Root Directory.
Connect that project to the `stepandel/cantelop` repository through Vercel's
GitHub integration. The integration is the console's CI/CD path: pushes to
non-production branches create preview deployments and pushes to the configured
production branch create production deployments. Protect and verify that
production-branch setting in Vercel before a coordinated release; the
repository contains `console/vercel.json` but does not encode the GitHub
integration or production-branch policy.

Vercel installs the Bun dependency graph from `console/bun.lock`. The checked-in
build command applies the control and observability PostgreSQL migrations, in
that order, before building the Next.js application. A migration failure fails
the deployment, and production refuses to build unless `CANTELOP_DATABASE_URL`
and a valid `CANTELOP_OBSERVABILITY_INGEST_TOKEN` are configured. By default
both schemas use the control connection; an optional observability URL moves
telemetry to another PostgreSQL database. Previews
either receive an isolated control database URL and migrate both schemas, or
receive no control URL and skip migrations; a preview must never use production
database credentials. A successful deployment does not replace
`make qualify-release`, deploy the durable control worker, or qualify the
Message lifecycle. Record the source commit and Vercel deployment identity in
the release cohort.

Configure a GitHub OAuth application and a Google OAuth 2.0 web client whose
production callbacks are:

```text
https://CONSOLE_HOST/api/auth/callback/github
https://CONSOLE_HOST/api/auth/callback/google
```

Set the variables listed in `console/.env.example`; both provider credentials
are required, and the console fails to start without either pair. Users are
resolved locally into PostgreSQL using the provider name and the stable
provider account ID, so one person signing in with both providers is two
separate users. Google sign-in is refused unless Google reports the address
as verified. Customer runtime
telemetry reads the existing observer from the console server and
intersects every snapshot and request-history query with Workspaces owned by
the signed-in user in PostgreSQL. The observer credential is never sent to the
browser, and unowned Workspace IDs are never forwarded to the observer.
`ADMIN_EMAIL` grants the separate `/admin` operator view. The account that
signs in with that verified address is the default administrator, but its first login still creates
an ordinary real user; there is no seeded or synthetic admin row.
`FIRE_FUSE_OBSERVER_URL` is the observer origin used for server-side customer
and operator reads, ownership-checked Session termination, and admin-only
Sandbox stopping and crash injection. The console derives `/snapshot`,
`/requests`, `/infrastructure`, `/apps/{id}/builds`, `/sessions/{id}`,
`/sandboxes/{id}`, `/sandboxes/{id}/crash`, and
`/sandboxes/{id}/database-access` from that origin.
`FIRE_FUSE_STATUS_TOKEN` authenticates reads and `FIRE_FUSE_RUNTIME_TOKEN`
authenticates Session termination, Sandbox stopping, and crash injection. Both tokens must match the host observer
configuration and must be distinct.

The console uses Bun, pinned through `packageManager` and `console/bun.lock`.
Vercel detects that lockfile when `console` is configured as the project Root
Directory.

The dashboards are dynamically rendered with one initial snapshot, then poll
separate same-origin customer or admin routes every five seconds. Customer
product routes read PostgreSQL locally; runtime and request reads use the
server-side observer client and filter against PostgreSQL ownership before
returning data. Server-side admin telemetry routes call `/snapshot` and
`/infrastructure` with `cache: no-store`; the observer bearer token
is never sent to browser code. **Refresh now**
triggers the same bounded reads immediately.

Request history is loaded from SQLite in pages using `before_sequence`.

## Local verification

Run the observer against a local `fire-fuse` instance, then set
`FIRE_FUSE_OBSERVER_URL=http://127.0.0.1:8082` in `console/.env.local`.
Set the read token and the distinct mutation token to exercise telemetry and
Session termination through the same observer origin.
Plain HTTP is accepted only for localhost development. Production observer
URLs must use HTTPS.

Install and run the console from its directory:

```sh
bun install --frozen-lockfile
bun run dev
```
