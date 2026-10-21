# Application-owned mailbox contract

SDK 1.0 is the only supported SDK. This is the current application-owned
messaging contract.

Keep the SDK mailbox in memory. The application owns its work queue, steering,
cancellation, persistence, and recovery. Fire Fuse owns durable transport,
Session routing, compute ownership, and safe release. Workspace database access
remains available independently of the messaging implementation.

The [retired Workspace Turso mailbox](../history/workspace-turso-mailboxes.md)
is historical. Its coordinated retirement is recorded in the
[October 6 rollout](../history/application-owned-mailbox-rollout-2026-10-06.md).
SDK 1.0 uses the in-memory runtime and independent Workspace database access.

## Ownership

| Component | Responsibility |
| --- | --- |
| Fire Fuse transport | Persist accepted envelopes, deduplicate client submissions, route to a Session, retry delivery until application acknowledgement, retain transport status and requested replies. |
| SDK runtime | Buffer envelopes in memory, invoke short serial receive handlers, deduplicate within an incarnation, track intake and background activity, report acknowledgement and quiescence. |
| Application | Decide queue, steer, cancel, replace, and priority behavior; persist jobs and checkpoints; deduplicate redelivery; resume unfinished work; decide application outcomes. |
| Compute lifecycle | Fence stale owners, enforce bounded work leases, drain output, stop admission before release, and terminate Sessions when requested. |
| Workspace DB service | Provision databases and renew authorized credentials; do not install or operate application queue tables. |

Transport ordering applies to delivery into a Session. It does not prescribe
the order in which application jobs run. The in-memory inbox may remain FIFO:
long-running agent work belongs in tracked activity, allowing subsequent receive
handlers to steer or cancel it promptly. A receive handler that awaits an entire
agent run will delay subsequent input and is unsuitable for that pattern.

## Acknowledgement contract

There are three separate milestones:

1. **Transport acceptance:** Fire Fuse commits the envelope before returning
   acceptance to the caller. This promises bounded delivery attempts, not an
   application DB commit or successful job execution.
2. **Runtime admission:** the envelope enters an incarnation's in-memory inbox.
   This receipt supports same-incarnation retry resolution. It is not sufficient
   to retire the durable transport envelope.
3. **Application acknowledgement:** the receive handler returns successfully.
   For persistent jobs, the application commits its queue update before returning.
   After acknowledgement, the application owns completion and recovery.

Successful receive completion is application acknowledgement. For a harness
runtime, the SDK separates command intake from provider work; request replies
wait for `queue` and its cleanup to settle. A required reply must also be supplied before
successful completion. A handler that fails is reported as a failed intake;
arbitrary handler exceptions are not automatically retried.

The transport persists acknowledgement before retiring delivery. Ambiguous
delivery or lost acknowledgement is resolved against the same live incarnation
first. If that incarnation is confirmed dead before acknowledgement is persisted and
the application opts in with redelivery: true, the transport may redeliver the same immutable message ID to a replacement.
Redelivery is bounded by transport expiry and attempt limits. Expiry or exhausted
attempts does not prove that the application never committed the message.

Applications needing durable work atomically insert a message receipt and the
corresponding job or control update in their own DB. Duplicate receipt handling
returns the stored intake result without repeating the update. Same-incarnation
SDK deduplication cannot replace this receipt across process loss. There is no
exactly-once guarantee for external side effects.

Existing public accepted, handling, handled, and failed states describe transport
and receive processing. Handled does not imply a background application job has
finished. Applications expose job status themselves. A request that starts durable
background work should return a job ID; synchronous request replies remain
supported, but occupy the receive handler until completion.

Public message status exposes the exact transport execution phase, including
retrying and recovering. API bundles and Session runtimes use the matching current
SDK. There is no projection for older SDK phase enums.

## Application execution

A reference application uses a short receive handler to validate and deduplicate
the message, commit a queue or control update, start or notify tracked activity,
and return its intake reply. Queue, steer, and cancel are application message
types rather than platform message states.

Persistent queued work must be loaded on every fresh application activation,
including normal activation after parking. Use onActivate, which runs once before the first intake or recovery in each
incarnation. On an unexpected owner loss,
recovery restores application checkpoints; it does not infer job completion from
transport handler settlement. Only start replacement work after the old owner
is confirmed terminated or effectively fenced.

The application must keep activity registered while its worker or queue pump is
running. A successful acknowledgement never authorizes parking by itself. Idle
means no pending intake, no receive handler, no tracked activity, and no outstanding
output handoff. Release closes admission and verifies the current generation and
exact owner/lease. A racing message either prevents release or remains in transport
for a fresh activation.

Local context.send is in-memory delivery. Applications that require internal
follow-ups to survive process loss persist those follow-ups themselves. Future
scheduled jobs also need an explicit wake source; a row in the application DB
alone does not activate compute. This migration does not add a scheduler.

Application cancellation is cooperative and application-defined. Session stop,
lease expiry, and platform deadlines remain lifecycle operations that can abort
or terminate runtime work. Cancelling a transport intake cannot retract an
application job that was already acknowledged. Explicit Session stop atomically
withdraws the current Sandbox route and settles its transport executions before
runtime termination. It preserves acknowledged outcomes and retained replies,
fences pending redelivery and stale coordinator writes, and suppresses loss
recovery. A later new message may activate a fresh Sandbox for that Session.

## Compatibility retirement

The coordinated cutover is complete; see the
[October 6 rollout record](../history/application-owned-mailbox-rollout-2026-10-06.md).
The SDK exports only its in-memory runtime and independent database access.
The explicit mailboxDatabase injection, durable runtime, SQL store, and memory
adapter for the retired storage contract are removed.

Fire Fuse has one execution dispatcher and one transport admission path. Console
has no Workspace mailbox endpoint or migration command. The old coordinator,
mailbox wake listener, history reader, durable capability, ownership receipts,
mode flags, and shared SQL qualification are removed. Retirement drops the three platform-owned tenant mailbox tables
(messages, sessions, and schema) and their index. Application-owned tables and
Workspace databases remain intact. Old message IDs and replies are no
longer served through platform status or request APIs.

Deployment must upgrade selectable API artifacts before exposing the exact phase
enum. Keep source repositories unchanged when rebuilding their bundled SDK is
sufficient, including Doop. Fence ingress and drain existing transport work before
switching the verified SDK and platform cohort. Preserve Fire Fuse rollback snapshots; a
rollback requires restoring a matching in-memory cohort under maintenance. Once
retired tenant tables are dropped, the DB-mailbox runtime is no longer a rollback
option.

## Required verification

- Workspace DB credentials do not select or require DB-backed mailbox tables.
- A running tracked agent receives subsequent queue, steer, and cancel input.
- Lost admission responses deduplicate against the same live incarnation.
- A crash after an application DB commit but before persisted acknowledgement
  redelivers the same ID and produces one application queue or control update.
- Lost acknowledgement after a completed intake preserves the same reply through
  application deduplication and transport reconciliation.
- Ordinary activation and crash recovery both restore unfinished application jobs.
- A queued transport message and active application work each prevent unsafe
  parking; a message racing with release is delivered on a valid owner.
- Confirmed old-owner termination precedes replacement execution; stale lifecycle
  evidence cannot release or acknowledge the replacement owner's work.
- A failed receive handler, expired delivery, acknowledged job, and completed job
  remain distinguishable in status and application behavior.
- Local development, requested replies, output acknowledgement, explicit Session
  stop, and Workspace DB authorization continue to work.

Qualify matching SDK 1.0 and platform artifacts through the
[deployment runbook](../deployment.md) before selecting them for a live release.
