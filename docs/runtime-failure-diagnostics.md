# Runtime inspection and workload failure evidence

Runtime inspection now retains a bounded diagnostic on the durable execution
record: `category`, sanitized `error`, `first_failure_at`, `last_failure_at`,
`consecutive_failures`, and `last_success_at`. First failure means the beginning
of the current (or most recent) streak. Successful inspection resets the count
and retains the prior failure for investigation. Dispatcher restarts retain this
history. Failure/recovery logs carry these fields into the originating attempt's
trace. Raw URLs, response bodies, and arbitrary exception strings are excluded.

For settled Messages whose activity and work deadlines are unchanged, successful
inspection timestamps are checkpointed at most once every 30 seconds. Runtime
checks still run each pass. Failures, recovery, and changed supervision metadata
are persisted immediately, so a live activity's old commands do not require
repeated writes on every sequential request.

Both dispatcher inspection points enforce the earliest supervision deadline plus
ten seconds of cancellation grace. Leases stay renewed during inspection outages.
An unresolved inspection failure terminates the pinned sandbox with
`runtime_unreachable` and an incomplete attempt; it does not infer OOM or process
death from a timeout, HTTP error, or protocol mismatch. A settled launch-message
receipt remains separate from the outcome of its long-running activity.

## Guest evidence

The assigned-workload systemd service runs
`fire-fuse-record-workload-exit` before poweroff. This records systemd's
`SERVICE_RESULT`, `EXIT_CODE`, `EXIT_STATUS`, and the workload cgroup's
`memory.events` OOM-kill count through journal+console, independently of SDK
telemetry. The helper is installed by `prepare-guest.sh`; existing guest images
and snapshot templates must be rebuilt before it is available.

- `oom_confirmed` records systemd OOM termination or an observed OOM kill in the
  workload cgroup. A counter alone does not identify the victim or establish that
  OOM caused the eventual workload exit.
- `workload_exit` records the service exit without confirmed OOM evidence.
- A missing counter is `null`, not zero. SIGKILL alone does not establish OOM.

The journal record is diagnostic evidence, not an authenticated execution-trace
input. Automatic correlation of guest records into terminal execution reasons is
still outstanding. Do not classify execution failures by parsing arbitrary guest
stdout: application output can imitate those records. Existing host-side
Firecracker exit records describe the VM process, not the SDK or its child.
Abrupt host/VM loss can prevent the guest exit hook from running at all.

## Verification and incident limits

Dispatcher tests cover categorized inspection failures at both inspection points,
restart/recovery history, sanitized trace records, cancellation grace, lease
release, and absence of message readmission. SQLite coverage verifies diagnostic
round-trip persistence. Guest tests verify exit-record sanitization, absent OOM
counters, and systemd hook ordering; they are fixture tests, not Firecracker OOM
reproductions.

The SDK repository contains lifecycle telemetry and a tested
`examples/supervised-activity` pattern for cancellation, output, persisted
outcomes, and queued follow-up prompts. Its Bun subprocess tests cover startup
failure, unexpected exit, stdout/stderr, backpressure, cancellation, runtime
health, and SDK process death.

None of these changes establishes the September 11 production root cause.
Production guest kernel/systemd logs are still needed. The reported Docker OOM
results remain supporting evidence rather than a reproduction of the complete
Firecracker failure. Memory/profile changes and the OpenCode app are outside this
change.
