# Workspace activation gateway

`fire-fuse-gateway` is the private request broker between the Cloudflare edge
and the loopback Fire Fuse control plane. It is not a public hostname router.
The SDK-generated App Edge Worker selects a concrete Workspace for each runtime operation;
the gateway authenticates that decision, verifies that the Workspace belongs to
the supplied App and durably accepts actor Messages for asynchronous delivery.

The complete request-coordination path is:

```text
<app-slug>.cantelop.dev
  -> public dispatcher
  -> generated App Edge Worker (selects Workspace)
  -> outbound Worker
  -> authenticated origin broker
  -> Access-protected Cloudflare Tunnel
  -> fire-fuse-gateway on 127.0.0.1:8081
  -> fire-fuse durable Message delivery
  -> fire-fuse Message dispatcher
  -> selected Session and its Sandbox mailbox
```

There is no direct Workspace hostname, public gateway listener, or fallback
hostname resolver. Workspace identity crosses the private edge boundary as an
explicit ID and is always checked against the App identity before allocation.

## Responsibility boundaries

Request coordination is divided among these actors:

1. The public dispatcher validates the canonical App hostname, resolves the App
   ID, removes reserved headers, and invokes the App's customer API Worker.
2. The generated App Edge Worker uses the SDK runtime API and chooses a Workspace.
3. The outbound Worker validates the SDK call and constructs a complete App,
   Workspace, hostname, and request dispatch target.
4. The origin broker adds origin credentials and carries the request through
   the protected Tunnel. It never chooses or defaults a Workspace.
5. The gateway authenticates edge-broker metadata, validates App/Workspace
   ownership, handles Workspace and Session control operations, and durably
   accepts actor Messages.
6. The shared Session broker maps application identities to internal Session
   identities and owns create, keyed open, logical open, resolve, keep-alive,
   and release semantics. It is an in-process component used by both request
   coordinators, not another service.
7. The Message dispatcher claims durable platform deliveries, asks the Session
   broker to open their Sessions, and invokes the Message delivery service without
   re-entering gateway HTTP routing. Workers serialize delivery per logical
   Session.
8. The Message delivery service owns Sandbox delivery, handler settlement, and
   the work lease that protects the Sandbox until SDK runtime quiescence. It also
   continuously drains asynchronous SDK Session output into the platform event
   broker, including during quiescence recovery.
9. Fire Fuse owns Session records, Sandbox allocation, durable Message delivery
   projections, capacity admission, routing, idle expiry, and
   recovery. It does not mirror the SDK mailbox.
10. The SDK Session runtime owns the volatile in-Sandbox mailbox, application-level
    behaviour, managed activity, settlement, and runtime quiescence signals.
11. Fire Fuse owns bounded encrypted Session event replay in its host state
    database and assigns the trusted Session sequence. The gateway adapts that broker
    contract to SSE and output-only WebSockets.

The gateway intentionally has no customer-defined API-provider implementation
and no direct hostname compatibility mode. Cloudflare and the customer API
Worker own public routing; Fire Fuse owns compute lifecycle.

## Private edge contract

The origin broker supplies exactly one copy of each reserved header:

```text
X-Cantelop-Edge-Dispatch
X-Cantelop-Edge-Authorization
X-Cantelop-Edge-Request-ID
X-Cantelop-Edge-App-ID
X-Cantelop-Edge-Hostname
```

The dispatch marker is `1`. The authorization value must match the gateway's
dedicated edge-ingress token. App ID, request ID, and canonical public App
hostname must be structurally valid. The Workspace named in the request body
must belong to the dispatched App. The
gateway consumes the reserved edge headers; they never enter the persisted
Message payload or the Sandbox request.

SDK 1.0 is the only supported SDK. The generated App Edge accepts backend
commands at `/commands/{type}`, checks runtime identity, and calls the private
integration v2 transport for Session operations. Workspace resolution and
database access retain private v1 routes. See the
[Edge transport routes](edge-workers.md) and [SDK 1.0 contract](sdk-1-platform.md).
Unknown routes fail before VM allocation.

`app.workspace({ slug })` is a lazy reference. Its explicit `resolve()`, dispatch,
or request materializes that exact slug when absent. Session status, stop, stream,
and view only resolve existing Workspace/Session bindings. Every selector names
exactly one Workspace ID or slug; no default is inferred from hostname metadata.

The Message envelope carries the application-scoped logical Session ID,
concrete Workspace ID, developer-owned payload, and idle lifetime. The Session
broker derives the internal `ses_…` resource ID from the App and logical Session
IDs. Concurrent callers using the same identity converge on one Session, while
a conflicting Workspace selection is rejected.

## Message delivery

`POST /__cantelop/integration/v2/messages` validates App and Workspace ownership, encrypts
and persists the payload in Fire Fuse, and returns `202 Accepted` without
allocating a Sandbox. Acceptance is therefore durable at the platform delivery
layer. The SDK Session runtime mailbox, its FIFO contents, and its deduplication records
remain volatile and are not copied into Fire Fuse.

The platform record exposes `accepted`, `handling`, `handled`, or `failed` to
the SDK Message reference. Separately, its internal work state records whether
delivery is pending, active, awaiting runtime quiescence, quiescent, or
unavailable. Handler settlement and Sandbox releasability are deliberately
different facts: the Message delivery service retains an expiring Session work lease
until the Session runtime confirms quiescence for the generation returned with the
settlement response.

Retryable Session activation and uncertain delivery errors back off with jitter
while retaining the Message's exclusive `handling` claim. The retry budget is
six attempts or 30 seconds from durable acceptance, whichever occurs first.
Exhaustion records the safe terminal error `delivery_retry_exhausted` instead of
leaving the Message pending indefinitely.

`GET /__cantelop/v1/sessions/{logical_session_id}/messages/{message_id}` reads
the durable projection after verifying App, Workspace, and logical Session
ownership. A missing or mismatched record returns `unknown` without exposing a
Message owned by another Session. Fire Fuse does not expose mailbox depth,
ordering, internal self-generated Messages, or activity objects.

## Session event delivery

The Session runtime exposes a private, backpressured event drain. The Message executor
starts with a nonblocking snapshot and then long-polls that drain while handler
or managed-activity work remains live. Events are appended to the control
plane's persisted event broker before the Session runtime cursor advances. The
protocol-3 drain is mandatory; mismatched runtimes are rejected rather than
served through a compatibility path.

The broker keys data by Workspace and logical Session, assigns one monotonic
sequence, and records the originating Message ID and platform timestamp. The
current schema-26 implementation persists stream identity, source fingerprints,
and the latest 256 encrypted application events per Session. Host restart retains
replay. Global stream, fingerprint, and ciphertext bounds prevent unlimited
retention; capacity can reclaim empty reservations, closed streams, and old
committed views.

A stale sequence returns `event_cursor_expired`; a different or evicted stream
identity returns `event_stream_reset`. Recovery must use application state rather
than assume unlimited history. Reads do not activate compute. See the
[SDK 1.0 replay implementation](sdk-1-platform.md#sdk-work-commands-and-durable-projections)
for capacity and projection behavior. Durable views are not yet available on
hosted Cantelop.

SDK JSON request bodies have a 10-second read deadline in addition to their
byte limits. The deadline is cleared once the body is fully read; it does not
limit subsequent Session work, SSE subscriptions, or WebSocket lifetimes.

`session.stop()` releases the allocated Sandbox and leaves the logical Session
reusable. Workspace data persists. End live subscriptions and recover using an
explicit cursor or an application snapshot when the Session activates again.

`GET /__cantelop/integration/v2/sessions/{logical_session_id}/events` requires the SDK-owned
Workspace query and either `Accept: text/event-stream` or a WebSocket upgrade.
SSE immediately writes and flushes a `: connected` comment after validating the
subscription, even when no events are available. This avoids waiting for the
first event or heartbeat to deliver body bytes through intermediaries. The
comment has no event ID, does not advance the replay cursor, and indicates an
open subscription rather than Sandbox or agent readiness.

SSE IDs include stream identity and sequence (`stream_id:sequence`). WebSockets require subprotocol
`cantelop.events.v1`, preserve the same JSON envelope, and reject client data
frames. The preserved public `Origin` must match trusted edge hostname metadata
before the private gateway relaxes the library's same-Host check.

Subscriber cancellation ends only that gateway read. It neither releases nor
terminates the Session and does not count as Sandbox work. Explicit Session
termination closes the broker stream; reconnect otherwise resumes from
`Last-Event-ID` or `?after=`.

New Message admissions and runtime completions wake dispatcher reconciliation.
If a Sandbox is already being reconciled or all worker slots are occupied, its
pending pass is retried when a worker becomes available. Sequential Messages
therefore do not wait for the one-second supervision timer after worker release.

Settled Message receipts and replies are durable and are not polled again while
an activity keeps the Sandbox live. A new first-attempt delivery can use the
fast path alongside settled Messages. Runtime inspection, lease renewal,
deadline enforcement, and output collection continue until quiescence.

## Observability

The gateway logs rejected control boundaries without Message payloads. The
dispatcher logs terminal delivery failures by Message ID and stable error code.
Fire Fuse exposes the durable delivery projection and separately records
Sandbox warm-up and lifecycle state; the SDK mailbox contents remain private to
the Sandbox.

Every durable delivery mutation also appends a payload-free transition event in
the same SQLite transaction. Events carry the Message, Workspace, logical and
concrete Session, edge request, attempt, generation, state, work state, safe
error code, and timestamp needed for correlation and latency derivation. Event
history for terminal deliveries is retained for 30 days and pruned when new
Messages are accepted. Current projections and any pending or in-flight history
are never removed by observability retention.

## Deployment

The gateway and dispatcher participate in a coordinated Message-protocol hard
cutover. Follow the [Message rollout and qualification](./message-rollout.md)
for artifact identity, deployment order, canary recovery tests, evidence, and
the cohort-wide rollback boundary.

Run the gateway on loopback next to Fire Fuse:

```sh
/usr/local/bin/fire-fuse-gateway \
  -listen 127.0.0.1:8081 \
  -control-plane http://127.0.0.1:8080 \
  -control-token-file /etc/fire-fuse-gateway/runtime-control.token \
  -product-token-file /etc/fire-fuse-gateway/product-control.token \
  -edge-token-file /etc/fire-fuse-gateway/edge-ingress.token
```

Run the single active Message dispatcher as a separate service:

```sh
/usr/local/bin/fire-fuse-async-dispatcher \
  -control-plane http://127.0.0.1:8080 \
  -control-token-file /etc/fire-fuse-dispatcher/runtime-control.token \
  -observability-token-file /etc/fire-fuse-dispatcher/observability-ingest.token \
  -observability-spool /var/lib/fire-fuse-observability \
  -observability-spool-max-bytes 67108864 \
  -observability-spool-max-age 24h
```

The dispatcher has no public listener or edge-ingress credential. The packaged
systemd unit runs it as `fire-fuse-dispatcher`, separately from the
`fire-fuse-gateway` account, with access only to its own scoped runtime and
observability credentials.
Each process constructs its own stateless in-process Session broker over the
same authenticated loopback control client. The dispatcher constructs the
Message delivery service and serializes Message ordering per Workspace/logical Session
through atomic queue claims.

The gateway will not start without a dedicated 64-character lowercase
hexadecimal edge-ingress token. The Ansible role installs it at
`/etc/fire-fuse-gateway/edge-ingress.token`, owned by root and readable only by
the gateway group with mode `0640`. Do not reuse the Fire Fuse control-plane
token.

The dispatcher also requires a distinct 64-character lowercase hexadecimal
observability ingestion token. Store the same value as
`vault_fire_fuse_observability_ingest_token` in the protected Ansible inventory
and as `CANTELOP_OBSERVABILITY_INGEST_TOKEN` in Vercel Production. The role
installs it at `/etc/fire-fuse-dispatcher/observability-ingest.token`; do not reuse
the edge, control-plane, observer, or Session mutation credential.

The Cloudflare Tunnel connects directly to `http://127.0.0.1:8081` through its
outbound-only host connector. No gateway nginx site, wildcard Workspace DNS, or
Workspace TLS certificate is required.

`/healthz` and `/readyz` accept only direct loopback requests with a loopback
Host and no forwarding metadata. The Tunnel path is therefore limited to
authenticated SDK runtime operations, not operator health endpoints.

The gateway's runtime token is accepted only for the exact App/Workspace,
Session, Message submission/status, and event-read routes it calls. Its distinct
product-control token is sent only to the two console Workspace routes and must
match `CANTELOP_GATEWAY_PRODUCT_CONTROL_TOKEN`, never the provider-ingress token.
The dispatcher and observer likewise receive only their exact runtime routes.
Use `platform/ansible/playbooks/shared/control-credential-cutover.yml` to stop all old
readers, provision the complete credential set and identities, then restart the
services in dependency order.
