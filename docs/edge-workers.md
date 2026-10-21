# Cloudflare edge request path

This document defines the Cloudflare-to-Hetzner request contract. Customer API
Workers execute in an untrusted Workers for Platforms namespace and own
explicit Workspace selection; there is no default Workspace. Alternative
runtime providers remain out of scope.

## Hostnames

The Cloudflare dispatcher owns this canonical public hostname:

```text
<app-slug>.cantelop.dev
```

Internal App IDs retain their existing `app_<32 lowercase hex>` form. The
dispatcher resolves the canonical lowercase App slug through a Cloudflare KV
route registry populated by the durable control worker. It accepts no
uppercase, trailing-dot, port, reserved, or additional-label aliases.

Terraform-owned, more-specific exclusions keep console, control, status, and
infrastructure hostnames outside the dispatcher wildcard. The broker reaches
Hetzner through a separate Access-protected Tunnel hostname such as
`edge-origin.cantelop.dev`, which remains outside the public dispatcher path.

## Component responsibilities

The request path has one customer Worker and three platform Workers:

1. The public dispatcher validates the hostname, derives the internal App ID,
   removes reserved edge headers, creates a request ID, and invokes the API
   Worker through the App's release gate. It neither selects a Workspace nor
   holds origin credentials.
2. The customer API Worker receives the public request. Its SDK transport must
   select one concrete Workspace ID for a runtime operation and sends it in the
   request body, or in the `workspace_id` query parameter of a `GET` route.
3. The outbound Worker accepts only the SDK runtime routes and methods listed
   below, validates the dispatcher-controlled App/request context, and passes
   that context unchanged to the broker as the dispatch target.
4. The origin broker writes the dispatch target to trusted headers and forwards
   the request through the private origin transport. It never selects,
   resolves, substitutes, or defaults a Workspace.

A fourth platform Worker, `cantelop-edge-tail`, is attached to the dispatcher
as a Tail Worker consumer and publishes user-Worker logs as Request logs; it is
not on the request path.

The Hetzner gateway authenticates the broker, validates the request metadata,
loads the supplied Workspace, and authoritatively confirms that the
Workspace belongs to the supplied App before invoking the existing runtime
broker. This ownership check is validation of the API Worker's decision, not
Workspace selection.

Only the dispatcher has a public Worker route. Customer API Workers are
reachable only through the dispatch namespace, and the outbound and broker
Workers only through Worker bindings. Only the broker holds the credentials
used to reach the protected Tunnel hostname.

The private hops use Worker RPC methods. Cloudflare transfers `Request`,
`Response`, and their byte streams across these boundaries with flow control.
The concrete
Wrangler configurations and staging commands live in
[`../edge/workers/README.md`](../edge/workers/README.md).

## API invocation

The dispatcher supplies this unversioned value to the outbound Worker as
trusted dispatch context:

```json
{
  "request_id": "req_0123456789abcdef0123456789abcdef",
  "app_id": "app_0123456789abcdef0123456789abcdef",
  "hostname": "base-agent.cantelop.dev"
}
```

The hostname must decode to the same `app_id`. A Workspace ID is
intentionally absent because selection belongs to the API Worker.

## Dispatch target

The dispatch target the outbound Worker supplies to the broker is the same
`APIInvocation` value (`DispatchTarget` is an alias for it in
`edge/contracts/src/index.ts` and `fire-fuse/edgeprotocol/contracts.go`). All
three fields, `request_id`, `app_id`, and `hostname`, are required. It carries
no Workspace ID: the Workspace travels in the request body (or the
`workspace_id` query parameter of a `GET` route), and the Hetzner gateway
performs the authoritative App/Workspace check. An omitted or empty Workspace
ID is never interpreted as `default`, `primary`, or a request for the broker
or Hetzner to choose.

## Release gate

The dispatcher maps the slug to one App ID through the KV route registry, then
sends the request to that App's `ReleaseGate` Durable Object
(`edge/workers/dispatcher/src/release-gate.ts`). The gate stores the selected
namespace script and an open/closed admission flag. Scripts are per-release
`rel_<32 hex>` names selected by the control worker during release
activation; legacy `app_<32 hex>` script names are still accepted. Closing the
gate waits for already-admitted requests to finish. While a gate is closed or
has no selected script, the dispatcher returns `503` with the
`release_transition` error code.

## Response identity

Every response the dispatcher returns carries `X-Cantelop-Request-ID`, the
`req_` identity of that request in the App's traces. It is an observability
identity, not a credential: the console's route page uses it to open the trace
a request produced, and a client may log it to correlate its own records with
Cantelop's. Customer Workers cannot set it; the dispatcher writes it after the
Worker responds.

## Broker-to-origin metadata

The broker writes the validated dispatch target to these internal headers:

```text
X-Cantelop-Edge-Dispatch
X-Cantelop-Edge-Authorization
X-Cantelop-Edge-Request-ID
X-Cantelop-Edge-App-ID
X-Cantelop-Edge-Hostname
```

The broker removes any caller-supplied `X-Cantelop-Edge-Workspace-ID` and never
sets it; Workspace selection travels in the request body.

`X-Cantelop-Edge-Dispatch` is the literal value `1`. The authorization header
contains the broker's dedicated edge-ingress bearer credential; it never
reuses the runtime control-plane token or the end user's `Authorization`
header.

The broker also replaces `CF-Access-Client-Id` and
`CF-Access-Client-Secret` with its Service Auth credential before fetching the
Tunnel hostname. Those headers authenticate the Cloudflare Access boundary;
the edge-ingress bearer independently authenticates the Hetzner gateway after
the request traverses the Tunnel.

The dispatcher removes the five edge headers above and
`X-Cantelop-Edge-Workspace-ID` from the public request before invoking the
customer Worker, then sets its own `X-Cantelop-Edge-Request-ID`. The outbound
Worker removes those six and both Access headers from customer subrequests. The broker replaces rather than appends them, and the Hetzner
gateway consumes them before accepting the runtime operation. They are trusted
only on the authenticated broker-to-origin boundary and never enter a Message
payload or Sandbox request.

The SDK runtime method, path, body, and safe headers flow through the Worker
chain without buffering. The public App hostname remains authenticated routing
context even though the broker connects to a distinct Tunnel hostname; the
gateway does not proxy the public HTTP request into a Sandbox.

SDK 1.0 backends call the generated App Edge at `POST /commands/{type}`. The
Edge uses private transport routes under `https://runtime.cantelop.internal`;
these are broker/gateway operations, not public customer endpoints.

Workspace resolution and credentials use private v1 paths; inspection and
deletion use integration v2:

```text
POST /__cantelop/v1/workspaces/open
GET  /__cantelop/v1/workspaces/{workspace_id}
POST /__cantelop/v1/workspaces/database/credentials
GET  /__cantelop/integration/v2/workspaces?workspace_slug={slug}
GET  /__cantelop/integration/v2/workspaces?workspace_id={workspace_id}
DELETE /__cantelop/integration/v2/workspaces/{workspace_id}
```

Inspection does not provision a Workspace. It returns `{ workspace: null }`
for absent, archived, or foreign Workspaces. Deletion takes a canonical ID and
uses the dispatcher's trusted App identity with the product-control route
`DELETE /internal/v1/apps/{app_id}/workspaces/{workspace_id}`. The locked
repository query checks that the Workspace belongs to this active App before
archiving it and queueing the existing durable filesystem/database purge. ID
retries can requeue failed purges without targeting a new Workspace that reuses
the slug. The response `{ id, status: "deleting", hostArchived }` acknowledges
the request; host admission closure and data purge are distinct milestones.
Both SDK commands require app integration authentication and skip Sandbox
runtime compatibility checks. No account login or installed CLI is needed.

Local development supports inspection. Its DELETE route returns 501 with
`workspace_deletion_unavailable_in_local_dev`, preserving local dev data until
a durable purge coordinator is available there.

Session operations use integration v2:

```text
POST   /__cantelop/integration/v2/messages
POST   /__cantelop/integration/v2/requests
DELETE /__cantelop/integration/v2/sessions/{session_id}
POST   /__cantelop/integration/v2/sessions/{session_id}/requests
GET    /__cantelop/integration/v2/sessions/{session_id}/messages/{message_id}
GET    /__cantelop/integration/v2/sessions/{session_id}/events
GET    /__cantelop/integration/v2/sessions/{session_id}/view
```

Dispatch, steer, request, and auth resolve a canonical Workspace before
submission. Session-scoped operations require exactly one `workspace_id` or
`workspace_slug`; they never provision a missing Workspace. Event subscriptions
also accept `after` and `stream_id`. The generated Edge checks the backend's
runtime identity before forwarding Session commands. The integration v2 gateway
reuses durable transport admission and actor protocol 2; it does not interpret
application queueing or cancellation payloads.

View routing is implemented in the source contract but durable views are not yet
available on hosted Cantelop. The SDK's browser-relayed native auth terminal is
also not yet hosted; use structured link/code login for the packaged Claude
provider. See [SDK 1.0 platform support](sdk-1-platform.md).

The lower-level v1 transport handlers and `internal/runtimecontract/routes.json`
remain internal implementation checks. Their presence does not provide support
for older SDKs. Unknown private routes return `runtime_route_not_found` and
wrong methods return `runtime_method_not_allowed` before VM allocation.

On the host, `fire-fuse-gateway` reads the matching credential from
`/etc/fire-fuse-gateway/edge-ingress.token` and refuses to start if it is
missing or malformed. The private Tunnel connects directly to the loopback
gateway so only the broker can enter the authenticated edge dispatch path.

## Contract failures

Each boundary validates its own input and fails before VM allocation:

- A malformed or noncanonical public App hostname is an unknown public route.
- A malformed dispatcher invocation is an internal dispatcher failure.
- A missing or malformed Workspace ID is rejected by the Hetzner gateway as
  an invalid request.
- Missing broker authentication is an unauthenticated origin request.
- A missing App or Workspace, an App/Workspace mismatch, or an archived
  Workspace is a rejected API-provider routing decision.
- Origin connection and timeout failures are temporary gateway failures.

Public error responses do not disclose whether another App owns a supplied
Workspace. Worker-generated errors use a no-store JSON envelope of
`{"error":{"code":"..."}}`. Unknown or unconfigured Apps return `404`;
invalid internal provider results and unavailable downstream transports return
`502`; missing Worker configuration or a closed release gate returns `503`;
and an impossible local dispatcher failure returns `500`. Hetzner gateway
responses pass through the chain unchanged.

## Compatibility

The internal contract has no explicit version field while all components are
owned in one repository and deployed in a controlled order. Additive changes
must remain optional during a rolling deployment. An incompatible change is a
coordinated migration until independently deployed customer Workers require a
versioned compatibility policy.
