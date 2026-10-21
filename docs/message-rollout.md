# Message protocol rollout and qualification

The Message protocol is a coordinated hard cutover. Fire Fuse, the gateway,
the asynchronous dispatcher, the observer, the edge and console callers, and
every selectable application Build must form one reviewed release cohort.
SDK 1.0 is the only supported SDK. No component may fall back to the retired protocol, and no compatibility
translation belongs in live code.

Use the canonical [`deployment.md`](./deployment.md) runbook for the complete
operator sequence and CI/CD boundaries. This document is authoritative for the
Message-specific cohort, acceptance, evidence, and rollback contract.

## Release identity

Before deployment, record one immutable identity for each member of the cohort:

- the Cantelop source commit, Fire Fuse deployment revision, and checksums for
  the Fire Fuse, gateway, dispatcher, and observer binaries;
- the published `@cantelop/sdk` version, package integrity, and source commit;
- every selectable application Build ID, its container-rootfs SHA-256, and the
  SDK version resolved into that Build;
- the console, control-worker, and edge deployment revisions; and
- the Fire Fuse state schema and PostgreSQL migration set.

The SDK version recorded for a Build is an artifact attestation, not a runtime
protocol negotiation. A Build without the Message-capable SDK is ineligible for
the catalog. Changing any identity creates a new candidate and requires fresh
qualification.

`make qualify-release` records a dedicated race-enabled
`message-lifecycle` check in `evidence.json`. It proves the platform lifecycle
and recovery semantics without claiming that a host deployment occurred.

## Deployment sequence

Use a maintenance boundary so callers cannot cross a partially upgraded
cohort:

1. Require clean reviewed source, successful release qualification, and a
   published SDK package. Build and attest all application rootfs artifacts
   from that exact package before making their Build IDs selectable. Extract
   the qualified Fire Fuse archive into one revision-named, root-owned
   controller directory, verify its embedded `SHA256SUMS`, and point every
   host-binary inventory variable—including the dispatcher—at that directory.
2. Preview, apply, and repeat `playbooks/compute/stage-runtime.yml` while the
   serving cohort is still active. Staging touches only the isolated release
   directory and must report `changed=0` on repeat. Keep the attested canary
   Build ready.
3. Stop new public Message ingress and let already accepted work reach a
   terminal work state. Record any delivery that cannot drain.
4. Capture the retained Fire Fuse upgrade snapshot with
   `prepare-runtime-upgrade.yml`. Record its upgrade ID and database checksum.
5. Promote and activate the staged Fire Fuse release. Require
   `/readyz` and the expected state schema before installing dependants.
6. Apply `gateway.yml`. It installs and starts the checksum-pinned gateway and
   the single active asynchronous dispatcher as separate systemd services.
   Require gateway readiness and both units to remain active.
7. Apply the observer, control-worker, console, and edge releases from the same
   cohort. Run their read-only readiness checks before restoring ingress.
8. Make only the attested Message-capable Builds selectable, run the deployed
   qualifications below, and restore public ingress only after every required
   result passes.

Do not deploy a dispatcher ahead of its Fire Fuse schema, expose new callers to
an old gateway, or retain an Executor-era Build as an implicit rollback path.

## Staging acceptance

Run the following with a dedicated canary App and Workspace. Preserve sanitized
API responses, transition events, service journals, timestamps, and service
status with the release evidence.

### Application-owned mailbox acceptance

Qualify the in-memory SDK runtime together with durable Fire Fuse transport.
Use the [application-owned mailbox contract](architecture/application-owned-mailbox.md)
and the [completed cutover record](history/application-owned-mailbox-rollout-2026-10-06.md).
Every selectable API bundle and Session runtime must use the current SDK;
execution status reports `retrying` and `recovering` directly.

1. Verify database credentials do not select a DB-backed mailbox or install queue tables.
2. Keep application activity running while subsequent intake can steer or cancel it.
3. Kill an opted-in canary after its application DB commit but before acknowledgement.
   Confirm exact old-owner termination, bounded redelivery of the same ID, and one application update.
4. Kill an acknowledged canary. Preserve its reply and intake result; restore work through
   application activation or recovery without repeating intake.
5. Stop a Session explicitly before and after acknowledgement. Confirm stop intent
   fences retry and recovery, preserves acknowledged replies, and allows a fresh activation.
6. Restart the coordinator with accepted messages. Confirm durable transport resumes
   delivery without resetting attempts, deadlines, or application deduplication.

The retired Workspace mailbox endpoints, SDK adapters, history reads, and mode
flags are absent. The old platform-owned mailbox tables were removed; see the
[retirement record](history/mailbox-data-retirement-2026-10-06.md). Preserve
application-owned tables and the Workspace databases themselves.

### Complete lifecycle

Run these checks against the application-owned messaging cohort.

1. Send a uniquely identified Message through the public SDK path and require
   `202 Accepted` before a Sandbox is allocated.
2. Correlate the edge request ID, Message ID, Workspace ID, logical Session ID,
   concrete Session ID, delivery attempt, and mailbox generation across the
   gateway, Fire Fuse transition events, dispatcher, and observer.
3. Require the payload-free event sequence to contain `accepted`, `handling`,
   `settled`, and `quiescent` in order. Handler settlement alone is not a
   successful lifecycle result.
4. For a zero-keepalive Session, prove that the work lease is released and the
   Sandbox terminates only after the SDK reports quiescence for the settled
   generation.

### Recovery checkpoints

Run these only against the canary while public ingress remains stopped:

- Interrupt the dispatcher after persisting intended ownership and again after
  mailbox reservation but before ACK persistence. Restart it and require the
  original attempt/receipt to be recovered from the same Sandbox without handler
  re-execution. Only claims with no durable binding may be requeued.
- Interrupt the dispatcher after `settled` but before quiescence, restart it,
  and require it to resume the quiescence check without posting the Message to
  the mailbox again.
- Replace or restart the canary Sandbox so the settled generation is no longer
  available. Require the delivery to become `unavailable` and its work lease
  to release; it must not remain indefinitely in flight.

The deterministic loopback versions of these cases live in
`fire-fuse/runtime/httpapi/mailbox_sdk_integration_test.go`. They are a
release gate, while the deployed canary proves the actual SDK-bearing image,
systemd processes, Tunnel path, and host lifecycle.

## Evidence and decision

The rollout record is complete only when it contains:

- the release identities and checksums above;
- the pre-upgrade snapshot ID and checksum;
- the pre-production `evidence.json` and successful check logs;
- the canary request and resource identifiers, ordered transition events, and
  proof that no payload appears in events or logs;
- the application-owned mailbox acceptance and recovery results, including
  dispatcher restart, activity retention, exact lease release, interrupted
  handlers, and explicit stop;
- readiness and service status for Fire Fuse, gateway, dispatcher, observer,
  control worker, console, edge, and Tunnel; and
- the operator, maintenance window, approver, result, and rollback owner.

A timeout, missing correlation field, unexpected redelivery, premature Sandbox
release, or mixed artifact identity fails the rollout. Keep ingress stopped
while deciding whether to fix forward or roll back.

## Rollback boundary

Rollback is cohort-wide. Restore a retained SDK 1.0 cohort with its complete
artifact set, matching backend clients, and compatible Fire Fuse snapshot using
the normal rollback runbook. Older SDKs and DB-mailbox runtimes are unsupported
rollback targets. Messages accepted after a restored snapshot cannot be silently
discarded; reconcile them before rollback.

`rollback-runtime.yml` restores Fire Fuse and leaves it stopped. Verify the
restored cohort and its state before activation. Never pair a restored legacy
database with Message-era services, run protocol migrations in application
code, or treat an Executor-era image as compatible with a Message-era host.

## Current cohort

The supported SDK is stable **1.0.0**, using project schema **3**, CLI build
protocol **6**, integration protocol **2**, and actor protocol **2**. The current
Fire Fuse implementation uses state schema **26** and migrates schemas 24 and
25 on startup. `examples/hello` pins SDK 1.0.0. Record the actual deployed host
revision and schema in release evidence rather than inferring them from this
source contract.

The SDK mailbox is in memory; Fire Fuse owns durable delivery and the application
owns its work queue and recovery. Workspace databases provision on first database
access and are independent of messaging. The former Turso mailbox rollout is
[historical](history/workspace-turso-mailboxes.md), not a supported mode.

See [SDK 1.0 platform support](sdk-1-platform.md) for the artifact and projection
implementation. Durable views and browser-relayed native auth terminals are not
yet available on hosted Cantelop.
