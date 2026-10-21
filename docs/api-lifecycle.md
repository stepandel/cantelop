# App, Workspace, and Session API

The public model has three resources:

- An **App** owns its immutable Builds, Session runtime, capacity, and runtime policy.
- A **Workspace** is the instant-created, durable and addressable boundary
  within an App.
- A **Session** is a stable runtime identity for actor work in one
  Workspace. Its `active` or `idle` state is derived from whether its current
  Sandbox is allocated; neither state is terminal.

Sandboxes and runtime providers are private implementation surfaces. Every
response includes `X-Request-ID`; resource IDs are the idempotency boundaries.

This document describes two HTTP surfaces:

- The **console tenant API** (`/v1/apps…`, `/v1/workspaces`, and the
  `/api/cli/…` login routes), served by the console and called by the
  `cantelop` CLI with a short-lived tenant bearer token. Its schemas are in
  [openapi-console.yaml](openapi-console.yaml).
- The **fire-fuse host API**, which is private to each compute host and is
  never exposed by the console. fire-fuse serves it on two loopback
  listeners, each request authenticated by one of the host's scoped control
  tokens (see [Route reference](#fire-fuse-route-reference)). Its schemas are
  in [openapi.yaml](openapi.yaml).

## Workspaces

Customers manage Workspaces through the console tenant API. List all owned
Workspaces, optionally scoped to one App:

```http
GET /v1/workspaces?app_id=app_0123456789abcdef0123456789abcdef
```

Create a Workspace immediately:

```http
POST /v1/workspaces
Content-Type: application/json

{"app_id":"app_0123456789abcdef0123456789abcdef","slug":"staging"}
```

The `201 Created` response is the authoritative Workspace. It contains `id`,
`app_id`, `slug`, the App-owned `session_runtime`, timestamps, and optional
`archived_at`. Workspace IDs use the `wsp_…` prefix. Slugs are unique within an
App and cannot contain the reserved `--` sequence.

Provider registration continues asynchronously after creation: the console
projects the Workspace to its host with
`PUT /v1/internal/users/{user_id}/apps/{app_id}/workspaces/{workspace_id}` on
the fire-fuse provider ingress listener. This is a projection concern and does
not introduce Workspace lifecycle state.

On the fire-fuse control listener, `GET /v1/workspaces?app_id={app_id}` lists
one App's Workspaces (`app_id` is required) and `GET /v1/workspaces/{id}` reads
one. Host Workspace records also carry the owning App's `app_slug`. Trusted
App-scoped resolution uses
`GET /v1/apps/{app_id}/workspaces/by-slug/{workspace_slug}`, which returns one
record without materializing the App's Workspace collection. An App has no
default Workspace: every Session names its Workspace by ID or slug.
The trusted Edge SDK bridge exposes idempotent App-scoped materialization as
`app.workspace({ slug }).resolve()`: it returns the exact Workspace when present
and creates it when absent. Customer code cannot supply a different App
identity. Concurrent resolutions converge through the unique `(app_id, slug)` key.

## Sessions

Create and synchronously start a Session:

```http
POST /v1/sessions
Content-Type: application/json

{"id":"ses_editor_01","workspace_id":"wsp_editor","keep_alive_seconds":0}
```

The caller supplies the Session and Workspace identities. The control plane
derives the Build, profile, shape, and Sandbox configuration from the Workspace
and its App. Replaying the same Session ID for the same Workspace returns the
existing allocation; reusing it for another Workspace returns
`409 session_conflict`.

The `202 Accepted` response is synchronous with startup: the Session is
`active`. It exposes the derived allocation state, never the internal Sandbox
ID or host recovery details.

Use:

- `GET /v1/sessions?workspace_id={id}` to list a Workspace's Sessions.
- `GET /v1/sessions/{id}` to inspect one Session.
- `GET /v1/sessions/{id}/route` for the private gateway endpoint.
- `POST /v1/sessions/{id}/work-leases` to protect the current Sandbox while a
  Message delivery is active.
- `POST /v1/sessions/{id}/work-leases/{lease_id}/renew` to extend that bounded
  protection.
- `DELETE /v1/sessions/{id}/work-leases/{lease_id}` to release it and begin the
  configured idle period.
- `DELETE /v1/sessions/{id}` to release its current Sandbox idempotently. The
  Session remains `idle` and reusable.

The gateway accepts Messages without creating a Session. The dispatcher opens
the Message's logical Session when it claims the durable delivery. An idle
Session is reactivated on a new Sandbox under the same Session ID. After all
work through the delivered Message's runtime generation becomes quiescent, the
Session's configured `keep_alive_seconds` determines how long its warm Sandbox
remains attached.

SDK keyed Session opening requires exactly one of a Workspace ID or an exact
App-scoped slug; a request with neither is rejected as invalid. Slugs have no reserved values or platform-defined
aliases. Exact resolution uses the unique `(app_id, slug)` index rather than
scanning the App's Workspaces.

Every newly started Sandbox has 30 seconds to acquire its first Session work
lease. A route lookup alone does not clear that protection. The Message
delivery service acquires a bounded lease before contacting the SDK Session runtime,
renews it while handler work remains active, and releases it only after the
Session runtime reports quiescence for the settled generation. Once the last lease is
released, `keep_alive_seconds` is applied: `0` releases the Sandbox immediately,
while a positive value starts an idle deadline of that many seconds. Expired
leases are recovered by Fire Fuse and cannot protect a Sandbox indefinitely.

## Messages

The gateway exposes the trusted SDK Message surface and submits accepted work
to Fire Fuse's private loopback queue. `POST /v1/message-deliveries` durably
stores one Message before returning. Replaying the same Message ID with the
same immutable envelope is idempotent; conflicting reuse is rejected.

The separate dispatcher owns queue consumption. It atomically claims eligible
Messages, opens their logical Sessions, delivers them to the SDK Session runtime, and
records handler settlement before waiting for runtime quiescence. The durable
projection deliberately separates application state from Sandbox work state:

- `state` is `accepted`, `handling`, `handled`, or `failed`.
- `work_state` is `pending`, `delivering`, `awaiting_quiescence`, `quiescent`,
  or `unavailable`.

A Message enqueued with `reply_requested: true` has its handler reply stored
with `POST /v1/message-deliveries/{id}/reply`; the stored `reply` is returned on
later delivery reads.

Retryable activation and delivery failures use exponential backoff with jitter.
The claimed Message remains `handling` during that delay so another dispatcher
worker cannot immediately reclaim it. Delivery becomes terminally
`failed`/`unavailable` with `delivery_retry_exhausted` after six attempts or 30
seconds from acceptance, whichever occurs first.

Settlement therefore does not imply that the Sandbox can be released. A
settled Message remains `awaiting_quiescence` until the Session runtime confirms that
all managed activity through its returned generation has completed. If that
generation is lost during recovery, the delivery becomes `unavailable` rather
than being falsely reported as quiescent.

The loopback queue routes are control-plane implementation surfaces used by
the gateway and dispatcher: acceptance (`POST /v1/message-deliveries`), lookup
(`GET /v1/message-deliveries/{id}`), atomic claim
(`POST /v1/message-deliveries/claim`), requeue
(`POST /v1/message-deliveries/{id}/requeue`), reply storage, failure
(`POST /v1/message-deliveries/{id}/fail`), and restart recovery of handling
deliveries (`POST /v1/message-deliveries/requeue-handling`). Sandbox-bound
attempt ownership and lifecycle evidence, including settlement, quiescence,
and terminal unavailability, are persisted through
`GET`/`POST /v1/message-executions`, and incarnation-bound leases through
`POST /v1/pinned-work-leases`. Payload-free transition history is available
newest-first at `GET /v1/message-delivery-events`; Message payloads and SDK
mailbox contents are never included in that history.

## Session events

Session runtimes publish streamed handler output through a bounded in-memory
broker. `POST /v1/session-events` appends one event (with optional
`source_sandbox_id`/`source_cursor` for replay deduplication) to the stream
keyed by Workspace and Session. `GET /v1/session-events` reads events after a
cursor (`workspace_id`, `session_id`, and `after` are required), long-polling
for up to 15 seconds unless `wait=0`; an evicted cursor returns
`409 event_cursor_expired` with the earliest retained sequence.
`POST /v1/session-events/close` marks a stream closed. Streams are not durable
across fire-fuse restarts.

## Request events

`POST /v1/request-events` durably records one completed request against the
Workspace and, when activation succeeded, the Session. Only the operator
credential is scoped to record events:

```json
{
  "event_id": "01k20rqy9rm2h6avf7k3ce4n1t",
  "request_id": "req_0123456789abcdef0123456789abcdef",
  "workspace_id": "wsp_editor",
  "session_id": "ses_editor_01",
  "received_at": "2026-08-05T12:00:00.123456Z",
  "status_code": 200,
  "duration_ms": 12.345
}
```

Read newest-first pages with:

```http
GET /v1/request-events?workspace_id=wsp_editor&limit=100&before_sequence=12001
```

`app_id` may replace `workspace_id` to read across an App's Workspaces; the
console's `GET /v1/apps/{id}/logs` uses that form. Events retain no paths,
query strings, headers, bodies, or byte counts.

## Common errors

| Status | Code | Meaning |
|---|---|---|
| 400 | `invalid_request` | Invalid JSON or resource identity. |
| 404 | `not_found` | Workspace or Session does not exist. |
| 409 | `workspace_unavailable` | Workspace is archived. |
| 409 | `session_conflict` | Session ID belongs to another Workspace. |
| 429 | `admission_limit` | App-shared or host capacity would be exceeded. |
| 503 | `release_transition` | App release admission is closed; retry Session creation. |
| 503 | `runtime_unavailable` | Sandbox startup or termination failed. |

## fire-fuse route reference

fire-fuse registers exactly these routes. Credentials are the scoped control
tokens from `fire-fuse/runtime/cmd/fire-fuse/control_credentials.go`; the
`operator` token may call every route on both listeners. A valid token used
outside its scope receives `403`.

Control listener (`-listen`, default `127.0.0.1:8080`):

| Route | Other credentials |
|---|---|
| `GET /healthz` | none |
| `GET /readyz` | unauthenticated |
| `GET /v1/apps` | gateway, dispatcher, observer |
| `GET /v1/apps/{id}` | none |
| `GET /v1/apps/{app_id}/workspaces/by-slug/{workspace_slug}` | gateway |
| `GET /v1/workspaces?app_id=` | gateway, dispatcher, observer |
| `GET /v1/workspaces/{id}` | gateway, dispatcher |
| `POST /v1/sessions` | gateway, dispatcher |
| `GET /v1/sessions?workspace_id=` | observer |
| `GET /v1/sessions/{id}` | gateway, dispatcher |
| `DELETE /v1/sessions/{id}` | gateway, observer |
| `GET /v1/sessions/{id}/route` | dispatcher |
| `POST /v1/sessions/{id}/work-leases` | none |
| `POST /v1/sessions/{id}/work-leases/{lease_id}/renew` | none |
| `DELETE /v1/sessions/{id}/work-leases/{lease_id}` | none |
| `POST /v1/pinned-work-leases` | dispatcher |
| `GET`, `POST /v1/message-executions` | dispatcher |
| `POST /v1/message-deliveries` | gateway |
| `POST /v1/message-deliveries/claim` | dispatcher |
| `POST /v1/message-deliveries/requeue-handling` | dispatcher |
| `GET /v1/message-deliveries/{id}` | gateway, dispatcher |
| `POST /v1/message-deliveries/{id}/requeue` | dispatcher |
| `POST /v1/message-deliveries/{id}/reply` | dispatcher |
| `POST /v1/message-deliveries/{id}/fail` | dispatcher |
| `GET /v1/message-delivery-events` | observer |
| `POST /v1/session-events` | dispatcher |
| `GET /v1/session-events` | gateway |
| `POST /v1/session-events/close` | none |
| `POST /v1/request-events` | none |
| `GET /v1/request-events` | observer |
| `POST /v1/sandboxes` | none |
| `GET /v1/sandboxes` | observer |
| `GET /v1/sandboxes/{id}` | none |
| `DELETE /v1/sandboxes/{id}` | dispatcher, observer |
| `POST /v1/sandboxes/{id}/crash` | observer |
| `GET /v1/sandbox-usage` | observer |
| `GET /v1/host-capacity` | observer |

The Sandbox, usage, and capacity routes are internal host surfaces for
operators, the observer, and dispatcher cleanup; tenants see Sandboxes only
through the console's App-scoped monitoring routes.

Provider ingress listener (`-provider-ingress-listen`, default
`127.0.0.1:8084`), used by the console's compute client with the `compute`
token:

| Route | Purpose |
|---|---|
| `GET /healthz` | Liveness. |
| `GET /v1/internal/database-access/{id}` | Confirm a starting or running Sandbox and return its App and Workspace before database credentials are issued. |
| `PUT`, `DELETE /v1/internal/users/{user_id}/apps/{app_id}` | Register or archive an App's desired state. |
| `PUT`, `DELETE /v1/internal/users/{user_id}/apps/{app_id}/workspaces/{workspace_id}` | Register or archive a Workspace. |
| `DELETE /v1/internal/users/{user_id}/apps/{app_id}/workspaces/{workspace_id}/purge` | Purge a Workspace's host state. |
| `PUT /v1/internal/users/{user_id}/apps/{app_id}/configuration` | Replace the App's projected variables and secrets at a revision. |
| `POST /v1/internal/users/{user_id}/apps/{app_id}/prepared-builds` | Download, verify, and install a prepared Build rootfs. |
| `DELETE /v1/internal/users/{user_id}/apps/{app_id}/prepared-builds/{command_id}` | Cancel a pending prepared Build. |
| `GET`, `PUT /v1/internal/users/{user_id}/apps/{app_id}/active-build` | Read or select the App's active Build. Selection requires closed release admission. |
| `GET`, `PUT /v1/internal/users/{user_id}/apps/{app_id}/release-admission` | Read, close, or reopen Session admission around a release switch. |

Full fire-fuse schemas are in [openapi.yaml](openapi.yaml); console tenant API
schemas are in [openapi-console.yaml](openapi-console.yaml).
