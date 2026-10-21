# Observability tracing

Cantelop stores cross-runtime traces in the Console-owned PostgreSQL service.
By default it reuses the control database connection but keeps telemetry in the
private `cantelop_observability` schema, with its own Drizzle journal and
migration lock. `CANTELOP_OBSERVABILITY_DATABASE_URL` can move that schema to a
dedicated PostgreSQL database later without changing the storage model. Fire
Fuse's SQLite database remains control-plane state and is not the trace store.

The execution projection shown in Console follows a public request inward:

```text
Request
└── Message
    └── delivery attempt
        └── Sandbox
            ├── SDK spans and logs
            └── Session
```

The App is implicit in the App-specific page. A Request may produce multiple
Messages. Messages point to one logical Session, while each delivery attempt
points to the Sandbox that handled it; retries can therefore converge on the
same Session through different Sandboxes. Requests without Messages remain
visible as roots but are never assigned to a Session. Messages whose edge
observation has not arrived remain visible in a separate unlinked section.

## Data path

The platform adds an attempt ID and W3C `traceparent` to each private Message
delivery. The SDK keeps the application-visible Message as a plain immutable
value while its internal observer records the `session.receive` span and tees
JavaScript console output plus `process.stdout` and `process.stderr` into the
observation stream. Output in an active asynchronous Message context belongs to
its attempt and automatic receive span. Background output after runtime
initialization has no invented Message association and is shown at Sandbox
scope. Its private runtime endpoint exposes ordered SDK spans and logs only to
Fire Fuse.

The Fire Fuse dispatcher validates those records, adds authoritative App,
Workspace, Message, attempt, and trace identity, and atomically appends them to
a private host spool. A relay sends batches only to the compiled
`https://console.cantelop.dev/api/internal/observability/events` endpoint.
Console validates the typed event again and commits it to the central store.
Stable source/event IDs make retries idempotent.

The mailbox dispatcher reconstructs `message.accepted`, `attempt.started`, and
`attempt.routed` from the saved execution identity before collecting SDK events.
It writes those prerequisites as one batch, including on recovery after a
dispatcher restart. Successful quiescence and failed or lost executions emit the
corresponding terminal attempt event. Telemetry failures never prevent execution
or cancellation; SDK events without an observed attempt are dropped rather than
assigned to another Message.

Console returns HTTP 422 for events with incompatible identities or missing
trace prerequisites. The relay logs and discards batches rejected with 400,
413, or 422 so one bad batch cannot block all later Apps' traces. Network errors,
authentication failures, rate limits, and server errors remain retryable.

The edge dispatcher emits one `request.completed` observation after the user
Worker settles. It includes the minted Request ID, method, path without query,
status, and duration. Delivery uses `ExecutionContext.waitUntil`, so central
observability latency or failure cannot delay or replace the customer response.

The spool is transport state, not trace storage. The packaged service limits it
to 64 MiB and removes batches after 24 hours. A batch is removed immediately
after Console acknowledges it. If the spool is full, user Message execution
continues and the dispatcher reports the dropped observation through its own
service log.

## Credentials and storage

Console requires:

- `CANTELOP_DATABASE_URL`; observability reuses this connection by default;
- optionally, `CANTELOP_OBSERVABILITY_DATABASE_URL` and
  `CANTELOP_OBSERVABILITY_DATABASE_SSL` to move telemetry to a dedicated
  PostgreSQL database; verified TLS remains mandatory outside loopback;
- `CANTELOP_OBSERVABILITY_INGEST_TOKEN`, a dedicated 64-character lowercase
  hexadecimal ingestion credential; and
- `CANTELOP_EDGE_OBSERVABILITY_INGEST_TOKEN`, a distinct 64-character lowercase
  hexadecimal credential accepted only for events whose source is `edge`; and
- `CANTELOP_OBSERVABILITY_RETENTION_HOURS`, optionally overriding the 72-hour
  default with an integer from 24 through 720.

The runtime ingestion credential is installed on the Fire Fuse host at
`/etc/fire-fuse-dispatcher/observability-ingest.token`. It must differ from the
edge ingress, observer, session mutation, and control-plane credentials. The
dispatcher runs unprivileged and can write only
`/var/lib/fire-fuse-observability`.

The edge credential is installed only as the dispatcher Worker's
`OBSERVABILITY_INGEST_TOKEN` secret. Runtime credentials cannot publish edge
events, and the edge credential cannot publish runtime events.

Run the central migration independently from the control database migration:

```sh
cd console
bun run observability:db:migrate
```

Vercel runs both commands before its production Next.js build and requires the
control database URL and ingestion token there. The observability migration
uses the same database unless its optional override is configured. A preview runs both migrations
when an isolated preview control URL is configured; with no control URL it
skips both migrations. An observability override without a control URL fails
closed, and previews must never receive production database credentials. Either
migration failing stops the deployment.

## Retention

The first ingestion transaction in each hourly window claims the central
retention sweep. Logs, completed traces, request observations, ingestion
receipts, and orphaned tree nodes expire after 72 hours by default. Pending
delivery attempts retain their span structure until settlement so a long-lived
handler can still complete consistently; the next sweep removes it after the
retention boundary. Foreign-key cascades delete descendants atomically.

The 24-hour minimum prevents accidentally configuring a window too short for
ordinary incident response. Changing retention affects future sweeps and does
not require rebuilding application runtimes.

## Console

The App's **traces** section queries the most recent 100 Request roots or
unlinked Messages. It displays Request-first trees, delivery attempts,
Sandboxes, nested SDK spans, attempt and Sandbox logs, and the logical Session
at which each execution converges. The trace store degrades independently of
the existing runtime monitoring pages.

## Qualify changes to Message tracing

Run the dispatcher and relay tests and the Console observability tests. With a
disposable PostgreSQL database migrated using `observability:db:migrate`, run:

```sh
cd console
CANTELOP_TEST_DATABASE_URL=postgresql://localhost/cantelop_test \
  bun test core/observability/postgres.integration.test.mjs
```

This test generates batches through the actual Go dispatcher, checks that an
orphaned SDK span is rejected, ingests the complete lifecycle, replays the
prerequisites, and verifies the Request → Message → attempt → Sandbox/span tree.

For the mailbox tracing repair, deploy Console before the dispatcher/relay.
No database migration, SDK release, or application rebuild is required. The new
relay discards rejected backlog batches automatically; a historical backfill is
not required to resume tracing. Verify a new Message has an attempt, Sandbox,
and `session.receive` span and that the runtime spool drains after rollout.
