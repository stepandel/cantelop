# Mailbox protocol v2

SDK **1.0** is the only supported SDK. It uses CLI build protocol **6** and
actor protocol **2**. The current Fire Fuse implementation uses state schema
**26**, migrating schemas 24 and 25 transactionally on startup; pre-cutoff
schemas are rejected. Private runtime paths use `/__cantelop/v2`. Backends call
the generated Edge at `/commands/{type}` using integration protocol 2; Edge
calls the private `/__cantelop/integration/v2` transport.

Admission workers persist an intended Sandbox binding before any POST.
Reconciliation uses that Sandbox after a lost ACK or dispatcher restart.
Applications own durable jobs and idempotency. Redelivery after confirmed
Sandbox loss is opt-in (`redelivery: true`) and bounded by attempts and expiry;
it does not guarantee exactly-once side effects. See the
[application-owned mailbox contract](architecture/application-owned-mailbox.md).


## Private operations

Every request supplies `X-Cantelop-Sandbox-ID`. Every response supplies that
identity and `X-Cantelop-Message-Protocol: 2`. Validation precedes mutation.

| Method and path | Semantics |
| --- | --- |
| POST `/messages` | Reserve the ID, enqueue, and immediately return 202 with receipt |
| GET `/messages/{id}` | Read receipt and current execution state; 404 means absent in this sandbox |
| GET `/messages/{id}/reply` | Read the completed bounded reply for a request |
| POST `/messages/{id}/cancel` | Request cooperative cancellation outside the application FIFO |
| GET `/runtime/completions?after=N` | Optional completion generation change; long-poll up to 25 seconds |
| GET `/runtime` | Identity, protocol, generation, quiescence, active work deadlines and stream positions |
| POST `/runtime/activity/cancel` | Request cancellation of the identified activity |
| GET `/runtime/observations?after=N&wait=0` | Non-mutating telemetry read |
| GET `/runtime/events?after=N&wait=0` | Non-mutating application-output read |
| POST `/runtime/{observations,events}/ack` | Explicit `{through:N}` checkpoint, bounded by delivered evidence |
| GET `/runtime/quiescence?minimum_generation=N` | Generation-fenced quiescence; bounded wait |

SDK 1.0 advertises `completionNotifications: true`. The
completion counter starts at zero and advances after each handler settles,
including failure and timeout outcomes. It is independent of work generation,
telemetry, output acknowledgement, and background activity. A wait returns
`{generation:N}` on advancement or HTTP 408 after 25 seconds. Notifications wake
only the affected Sandbox's reconciliation; receipts and durable reply storage
still determine the result. The one-second full reconciliation sweep remains a
fallback and continues for runtimes without this capability.

A receipt contains `sandbox_id`, `message_id`, optional `attempt_id`, `sequence`,
`generation`, `accepted_at`, `deadline`, `state`, and optional
`cancellation_requested_at`. States are `queued`, `running`, `cancelling`,
`succeeded`, `failed`, and `timed_out`. A cancellation request never claims that
uncooperative code has stopped. Reservations/fingerprints survive until sandbox
retirement; capacity exhaustion rejects admission instead of evicting IDs.

The duplicate fingerprint canonically encodes `{message, session}` with object
keys sorted recursively and array order retained. Trace fields and retry deadlines
are excluded; a duplicate returns the original immutable receipt and deadline.
Counters for mailbox order, work generation, observations, and output are distinct.

The generated Edge forwards `session.request()` to private
`POST /__cantelop/integration/v2/requests`. It durably admits a reply-bearing Message and
waits for its handler result. It does not create a direct gateway-to-Sandbox
execution path. Requests retain FIFO ordering and the same pinned-sandbox,
deduplication, deadline, and unknown-outcome rules as asynchronous Messages.
The caller's `timeout_ms` bounds only the HTTP wait; disconnecting or timing out
does not cancel execution. Retrying the same Message ID retrieves the original
delivery and reply.

## Persisted control metadata

`POST /v1/message-executions` conditionally writes one attempt/sandbox ownership
record. `GET /v1/message-executions` lists unfinished work. The record and handler
projection update in one SQLite transaction. `GET /v1/message-deliveries/{id}`
also returns the execution record, including terminal evidence.
Request intent and replies are stored with the encrypted Message payload. Reply
bodies are absent from lifecycle events, status responses, and logs. The
dispatcher retrieves a reply only after the owning runtime reports successful
handler completion and persists it before projecting the delivery as handled.
Encrypted replies are retained for 30 days after terminal delivery activity;
an otherwise valid retry after cleanup reports `request_reply_expired`.

`GET /v1/sessions/{id}/route?sandbox_id=...` never creates a replacement.
`POST /v1/pinned-work-leases` acquires, renews, or releases a sandbox-specific lease.
The release operation cannot update a replacement sandbox's idle deadline.

The App message-status response includes `execution`: phase, phase timestamp,
handler outcome (including `unknown`), sandbox, fixed deadline, receipt, activity,
work state, and loss reason. SDK consumers expose this as `status().execution`.
Phase-change logs contain identity/deadline metadata without message payloads.
The console Message monitor displays the current durable execution projection,
including sandbox, phase, deadline, outcome, and background activity. Historic
transition rows remain immutable; `current_execution` is explicitly read-time
metadata rather than a claim about the old event. Trace spans remain diagnostic.

## Time and loss policy

Admission requests have a 10-second limit and a 30-second same-sandbox retry window.
The initial handler budget is five minutes **from the persisted intent**, including
admission and queue wait; starting it slightly before ACK prevents an uncertain
POST from resetting the budget. Cancellation grace is ten seconds. SDK self-messages
also have finite deadlines, surfaced in `/runtime` for host enforcement.

Activities default to 30 minutes. SDK code may explicitly choose or extend an
activity by up to 24 hours per request. Unlimited activities are not enabled in
this cohort. Heartbeats never extend the work budget. SDK timers and persisted
control deadlines independently bound execution; an event-loop stall requires
termination of the exact owning sandbox.

Telemetry is bounded best effort and never gates handler progress, output
collection, or lease release. Output handoff is cancellation-aware with a 30-second
limit. An output timeout can be ambiguous: persisted broker acceptance does not
guarantee subscriber receipt. Retained output is acknowledged only after the
broker accepts it.

The broker deduplicates `(source_sandbox_id, source_cursor)` within retained
events. Stream identity, encrypted replay, and source fingerprints persist
across host restart in schema 26. SSE IDs are `stream_id:sequence`; explicit
`after` requires `stream_id`. A mismatch reports `event_stream_reset`, and
expired history reports `event_cursor_expired`. Replay retains the latest 256
application events per Session; capacity eviction can reset a stream. This does
not provide exactly-once application side effects. See the implementation's
[projection and replay contract](sdk-1-platform.md#sdk-work-commands-and-durable-projections).

## Sandbox recovery

Session recovery is opt-in. A runtime advertises the capability when its SDK
behaviour defines `onRecover`. If a Sandbox exits while a Message execution or
its supervised activity is still outstanding, the dispatcher persists one
recovery identity for that lost Sandbox, releases its old leases, and activates
a replacement for the same logical Session and Workspace.

The replacement receives `POST /__cantelop/v2/runtime/recoveries` before ordinary
Message admission resumes. The request identifies the recovery operation and one
interrupted Message. The SDK serializes the hook with the Session mailbox and
deduplicates the recovery identity for that runtime incarnation. The dispatcher
holds a replacement work lease until the hook and any activity it starts become
quiescent and their output is handed off.

Recovery is at least once across replacement Sandboxes. The platform restores a
place to run and signals the loss; application code interprets its durable
Workspace state and decides how to continue. The original Message outcome remains
unavailable because the platform cannot infer whether application side effects
completed before the crash. Explicit release, cancellation, idle expiry, and
platform-enforced termination do not request recovery.

This cohort does not impose a durable recovery-attempt budget. A repeatedly
crashing recovery hook can therefore cause repeated replacement attempts; bounded
attempts and an exhausted state are deferred to the recovery-budget change.

## Release qualification and cutover

Run SDK build, tests, package qualification, platform/CLI suites, and the real SDK
integration tests (`CANTELOP_SDK_RUNTIME_TEST_MODULE` can select a built SDK module).
The integration injects ACK loss, restarts the dispatcher before acceptance is
persisted, and replaces a sandbox under the same logical Session while counters
restart at one. It verifies one execution and broker handoff in each incarnation.

SDK 1.0 artifacts use build protocol 6. Generated Edge/native identities must
match, and private runtime identity discovery rejects incompatible images
before sending a message. Rebuild every
selectable SDK image and qualify the guest assignment injection after snapshot
restore.

The runtime migrates schemas 24 and 25 to 26 transactionally; older schemas
require the cutoff procedure in [operations](operations.md). A binary rollback
requires a compatible state snapshot. Keep cohort and schema snapshots for rollback. The
assigned-workload systemd unit powers off on process exit and has no restart
policy: mailbox recreation must get a new sandbox identity.

For the current cohort and rollout record, see
[Message rollout](message-rollout.md#current-cohort).
