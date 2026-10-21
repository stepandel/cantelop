# SDK 1.0 platform contract

SDK **1.0** is released and is the only supported SDK. Install the stable
package with `npm install @cantelop/sdk`; npm's `latest` tag is `1.0.0`.
Earlier SDKs and 1.0 prereleases are unsupported. Use Cantelop CLI **0.15.2 or
newer** and Node.js **22 or newer**. Native development requires Bun **1.3.10**;
container development and deployment require Docker with `linux/amd64` support.

SDK 1.0 uses project schema **3**, CLI build protocol **6**, actor protocol **2**,
integration protocol **2**, and local database protocol **1**. These protocol
numbers are independent of the package version. See the
[SDK 1.0 release notes](https://github.com/stepandel/cantelop-sdk/blob/main/docs/release-notes-1.0.md).

This document describes the platform implementation. Durable Session views and
browser-relayed native auth terminals are included in the SDK contract but are
not yet available on hosted Cantelop. Hosted Claude Apps use structured
link/code login. Projection details below describe local development and the
platform implementation, not a hosted availability guarantee.

## Define and build

`cantelop.json` contains `{ "schema_version": 3, "definition": "src/cantelop.ts" }`.
The definition exports named Apps created with an App-agnostic `CantelopClient`.
Runtime, environment declarations, Dockerfile, webhooks, and homepage belong to
the App definition. The CLI discovers definitions through the SDK compiler;
it does not execute customer App modules during discovery.

`cantelop build`, `dev`, `deploy`, and `doctor` accept `--app <name>`; selection is
required for multiple Apps. Builds produce paired SDK-generated Edge and Sandbox
artifacts and a backend module in `.cantelop/client`. Environment and schema
metadata come from those artifacts rather than a customer-authored Edge API.
Older artifact parsing in the implementation does not make older SDKs supported.
Rebuild and deploy the backend with the App whenever runtime logic or types
change: different identities receive `session_runtime_mismatch` (409). Copy the
complete compiled client directory, including declarations and `types/`.

## Configure and release

The control schema includes `console/core/db/drizzle/0024_sdk_app_artifacts.sql`,
applied through the existing migration command. It adds nullable
native runtime manifests and allows the encrypted system integration token in
App configuration. Existing release rows remain valid.

The authenticated `POST /v1/apps/:id/integration` control route returns the App's
stable integration credential. The CLI records credentials, Edge URL, and runtime
identity in its private per-App SDK profile. Local development writes the `local`
profile; deployment writes `default`. `CANTELOP_PROFILE` selects a profile.
Concurrent CLI processes serialize profile updates. Credentials belong on the
customer's server and must not be included in browser bundles.

`GET /v1/apps/:id/integration` reads the same credential without creating one,
together with the Edge URL and rotation state; `cantelop app credentials` prints
it for a hosted backend's secret store. `POST /v1/apps/:id/integration/rotate`
replaces the token. The rotated-out token is kept, encrypted, in
`app_integration_rotations` for an optional grace period (default one day, at
most seven); while it lasts the configuration projection adds
`CANTELOP_INTEGRATION_TOKEN_PREVIOUS` and
`CANTELOP_INTEGRATION_TOKEN_PREVIOUS_EXPIRES_AT` to the Edge bindings, and the
generated Edge accepts either token until that instant. Edge manifests from a
rotation-aware SDK declare both names in `optional_bindings`; a grace period is
refused while the live release lacks them. Rotation status is `applied` once Edge
accepts a configuration at or after the rotation's revision, independently of
subsequent compute synchronization. Pending or failed rotations block new tokens;
`POST /v1/apps/:id/integration/rotate {"retry":true}` resends the existing token
without changing either token or the original grace-period expiry.

Token rotation uses `console/core/db/drizzle/0025_integration_token_rotations.sql`
and the SDK 1.0 generated Edge, whose optional bindings accept the previous
token until expiry. CLI 0.15.2+ accepts those manifests. Follow the normal
[deployment order](deployment.md) for schema and service changes.

Uploads validate Edge/native App identity and configuration together, persist
both manifests, and derive public routes from the generated Edge manifest.
Required declared configuration and secret/variable kinds are checked before
upload and before activation. Ordinary default values reach generated Edge code
and the runtime Docker layer. Integration token and keep-alive bindings reach
only the generated Edge Worker, including after configuration replacement or
release rollback. Workspace schema provisioning reads the native manifest.

## Request path and local development

A customer backend calls the generated App client, which sends the public command
envelope to the App's Edge at `POST /commands/{type}`, one path per command. Homepage `/` and declared webhook
routes are generated public ingress. Webhook handlers use the same
`app.workspace(...).session(...).method(...)` interface and verify provider
signatures in customer code.

The generated Edge calls the private integration v2 transport through the
existing trusted outbound broker and gateway. The gateway enforces App and
Workspace scope and forwards normal dispatch/request messages through existing
durable admission and actor protocol 2. Session stop, status, and event streams
reuse existing lifecycle behavior. Reading session state never opens a Workspace.
The dispatcher continues to forward generated public routes.

`cantelop dev` runs the generated Worker against the local gateway and actual
Sandbox runtime, sets credentials automatically, and separates local Workspace
storage by App. It watches the complete definition graph and publishes only
matching artifacts from one compilation per rebuild. Startup builds once.
Automatically discovered `db/schema.ts` and its imports are watched independently
of the App definition, including adding a schema to an existing project.
Homepage/webhook-only changes rebuild ingress without
restarting Sandboxes when native runtime metadata is unchanged.

## SDK work commands and durable projections

Dispatch, steer and cancel are opaque SDK command payloads in the same delivery
queue. The platform provides FIFO handoff, durable identities, retries, replies,
and status. It neither prioritizes delivery nor interprets cancellation.
The next envelope can be claimed after the previous Sandbox acknowledgment,
independently of its handler completion.

Runtime factories create one harness per Session incarnation with `queue`, `steer`,
and `cancel`. The harness owns input ordering, steering, active-operation
cancellation, and provider cleanup. The SDK tracks completion and keeps intake
responsive. Custom receive/activity runtimes remain supported.

`session.cancel()` takes no target message ID and delegates active-operation
selection to the harness. The harness signals that operation and preserves waiting
input; returning `false` reports idle. Each submission's completion includes its
work and provider cleanup. A repeated command ID reuses its result. Dispatch status
describes intake; request replies wait for `queue` to return.

The selector-bound generic request route at `POST /__cantelop/integration/v2/sessions/{id}/requests` resolves an existing Workspace/Session and forwards opaque payloads through ordinary delivery. It does not provision a Workspace or interpret cancellation; generic delivery may reactivate an existing Session. Gateway and local CLI enforce the same scope checks. Infrastructure stop and allocation fencing are unchanged.

Runtime code explicitly calls `context.view.commit(state)`. Supervised activity
can commit through its own `view` capability while it is active. The collector stores
that state and its event boundary in one transaction before acknowledging the
Sandbox. `session.view()` reads the last committed projection without provisioning
or activation, returning revision, update time, state, and cursor. Subscribe with
`session.stream({after: snapshot.cursor})`. Projection records advance the cursor
but are omitted from application events. A missing projection returns 404.

The host SQLite schema advances from 24 or 25 to 26 transactionally on
startup. Delivery acknowledgment is persisted separately from handling. A host binary rollback
requires restoring a compatible state snapshot or using a compatible binary.
Previously process-local replay cannot be recovered by the migration. Stream identity, encrypted
replay, source fingerprints, and encrypted views persist across host restart;
views survive replay retention and Sandbox stop. Workspace metadata purge removes
its stream/projection records. Local dev stores encrypted state and its private
key under `.cantelop/dev/<app>/`, retaining the Workspace directory for ID reads.

Replay retains the most recent 256 application output records per Session;
projection records do not consume that window. Stale cursors fail
explicitly. Global limits are 1,024 streams, 100,000 source fingerprints, and a
bounded ciphertext budget. Stream capacity reclaims empty reservations, closed
streams, and then the oldest committed views; an evicted view returns 404 and its
stream identity expires. Other capacity exhaustion rejects the commit. Views and event history remain specific to the assigned
host; cross-host transfer still requires the existing host-state transfer process.

Generated manifests advertise SDK steering/cancellation and durable view support.
The platform validates artifacts but does not route business commands by their
capabilities. Build protocol 6 and actor protocol 2 remain independent of the SDK release
version.

For platform releases, qualify the state migration and release activation
against a test database, deployed Worker, and configured Sandbox provider. Local
Worker/Sandbox and Docker qualification do not establish hosted-provider
compatibility.
