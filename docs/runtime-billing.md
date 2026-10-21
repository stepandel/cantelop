# Sandbox runtime accounting

An app's billing time is the sum of its measured sandbox process intervals.
Concurrent sandboxes each contribute time. Idle time while the process is alive
counts. Allocation queues, prepared-but-unassigned host resources, and cleanup
after process exit do not count.

The current warm-slot pool pre-provisions jail/network resources, not running
VMs. Pool hits and cold starts both launch a new process for the sandbox. The
launcher captures `started_at` immediately after successful `cmd.Start`, before
waiting for the Firecracker API socket. The process waiter captures `stopped_at`
after `cmd.Wait`, before lifecycle reconciliation or cleanup. Failed startup
attempts that launched a process therefore still have a measured interval.

## Persisted contract

Each sandbox JSON record contains `runtime_timing`:

| Status | Meaning |
| --- | --- |
| `pending` | Newly allocated; process launch has not yet been recorded. |
| `running` | Measured `started_at`; no observed exit yet. |
| `stopped` | Immutable measured start and stop timestamps. |
| `not_started` | Supervisor rejected startup before any process was launched; verified zero uptime. |
| `unknown` or missing | Insufficient evidence to calculate uptime. |

The API reports missing legacy timing as `unknown`. Timing writes are idempotent,
do not modify `updated_at`, and cannot overwrite a completed interval. Cleanup
may advance the lifecycle record without changing uptime. No duration is inferred
from `created_at`, `updated_at`, startup duration, idle deadlines, or request logs.

A failure to persist process start aborts startup. The process exit callback
attempts persistence immediately; runtime reads and cleanup checkpoints retry
using the original timestamps. Cleanup cannot discard the tracked process before
its interval is saved. If the host/runtime exits before an interval can be closed,
startup recovery marks it `unknown`; recovery time is never a substitute exit time.
Completed intervals remain measured across restarts.

## Console and historical records

The sandbox table shows **Uptime**, or **Unknown** for unmeasured records. The
creation timestamp is labeled **Created**. Billing time stays in the compact
header: a complete measured sum is shown normally, a partial sum has a `≥` prefix,
and entirely unknown history reads **Incomplete**. A tooltip gives the count of
excluded records. Missing telemetry is distinct from verified zero usage.

Concurrency and uptime-distribution charts use the same measured intervals and
show their unknown-record count. A measured failed startup can contribute to the
uptime histogram even though it never reached readiness. A completed interval
ends at process exit, even if lifecycle cleanup is still in progress.

Historical records are deliberately not backfilled by this change. In particular,
the September 21 records clustered near 12:50 PDT do not have sufficient evidence
in their lifecycle timestamps to reconstruct process exits. Before any historical
repair, preserve the original database and correlate the full sandbox ID, process
identity/boot ID, and independent process-start/exit logs. Request completion or a
later bulk-cleanup timestamp is not proof of process exit. If either boundary
cannot be established, keep the interval unknown. This PR does not change a
production database or claim that historical totals have been recovered.

## Rollout and rollback

1. Keep a runtime database backup and deploy the runtime and observer together.
2. Verify a fresh successful sandbox, a startup failure, a crash, and delayed
   cleanup: only the measured process interval should contribute time.
3. Deploy the console. It safely treats older runtime responses without timing
   as unknown, so reversing this order does not fabricate usage.
4. Check that billing advances while alive, freezes at exit, and remains fixed
   after cleanup and service restart. An interrupted open interval should become
   unknown and make the app total incomplete.

The field is additive JSON in the existing SQLite sandbox records; no SQL schema
migration is required. Older runtime binaries use strict JSON decoding and cannot
read the new field. A runtime rollback must retain the new timing-aware reader;
do not simply deploy an older binary against records written by this version.
Console rollback should also retain the corrected accounting helper, rather than
restoring lifecycle-age billing.
