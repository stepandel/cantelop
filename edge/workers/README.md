# Cantelop edge Workers

This directory contains the Cloudflare request chain:

```text
public dispatcher -> user API -> outbound Worker -> origin broker -> Access + Tunnel -> Hetzner gateway

The dispatcher also has `cantelop-edge-tail` as a Tail Worker consumer. It
receives `console.*`, uncaught exceptions, diagnostics-channel events, and the
invocation outcome for every user Worker in the dispatch namespace. The
dispatcher supplies its minted Request ID in a reserved internal header so the
Tail Worker can publish those records to central observability as Request logs.
The central 72-hour retention policy applies after ingestion.
```

The dispatcher is the only public Worker. It invokes user APIs by stable App
ID from the staging dispatch namespace and supplies trusted App/request context
to the outbound Worker. The outbound Worker and broker have no public targets.
RPC transfers request and response streams between platform Workers.

The `outbound` Worker is the controlled egress boundary for Workers for
Platforms user code. Customer code reaches a VM
by fetching `https://runtime.cantelop.internal/...`; the Workspace is selected
in the request body. The outbound Worker strips reserved edge headers and
attaches dispatcher-supplied App context before calling the origin broker. Other requests are limited to public HTTPS destinations; Cantelop,
private, link-local, reserved, credentialed, and non-standard-port URLs are
denied, and redirects are returned manually so every destination is checked.

## Verify

From this directory:

```sh
bun install --frozen-lockfile
bun test
bun run typecheck
bun run test:fleet-runtime
WRANGLER_LOG_PATH=/tmp/cantelop-wrangler.log bun run build:broker
WRANGLER_LOG_PATH=/tmp/cantelop-wrangler.log bun run build:outbound
WRANGLER_LOG_PATH=/tmp/cantelop-wrangler.log bun run build:dispatcher
WRANGLER_LOG_PATH=/tmp/cantelop-wrangler.log bun run build:tail
```

The build commands bundle locally with Wrangler's `--dry-run`; they do not
modify Cloudflare resources.

Compatible Fleet packages use the dispatcher's `FleetPackage` Durable Objects
for immutable code and Worker Loader for separate installation isolates. The
release gate supplies installation bindings and per-invocation runtime
capabilities through private loopback entrypoints. Package publishing and App
configuration use the authenticated release-gate control origin. App deletion
retires its gate and purges its bindings; it cannot remove package code.
`test:fleet-runtime` runs the actual runner/gate under local workerd through Node
and checks isolation, configuration rotation, streaming, WebSocket draining and
retirement. It creates only disposable local runtime state. See
[Managed Fleets](../../docs/managed-fleets.md) for migration and rollout order.

## Staging bindings

Deploy the broker first, then set all four of its bindings as encrypted Worker
secrets:

```sh
bunx wrangler deploy --config origin-broker/wrangler.jsonc
bunx wrangler secret put ORIGIN_URL --config origin-broker/wrangler.jsonc
bunx wrangler secret put EDGE_INGRESS_TOKEN --config origin-broker/wrangler.jsonc
bunx wrangler secret put ORIGIN_ACCESS_CLIENT_ID --config origin-broker/wrangler.jsonc
bunx wrangler secret put ORIGIN_ACCESS_CLIENT_SECRET --config origin-broker/wrangler.jsonc
```

`ORIGIN_URL` is the exact HTTPS origin, for example
`https://edge-origin.cantelop.dev`, with no path, query, fragment, or embedded
credentials. `EDGE_INGRESS_TOKEN` must exactly match the 64-character
lowercase hexadecimal token installed on the Hetzner gateway. The two Access
values belong to a Cloudflare Access service token authorized by a Service
Auth policy for the origin hostname.

Deploy the private outbound Worker after the broker. It has no public route or
preview URL, and its only binding is the broker service:

```sh
bunx wrangler deploy --config outbound/wrangler.jsonc
```

Build the customer API with `@cantelop/sdk/build`, then upload its `worker.mjs`
under the stable App ID before deploying the dispatcher:

```sh
bunx wrangler deploy /path/to/worker.mjs \
  --name app_0123456789abcdef0123456789abcdef \
  --dispatch-namespace cantelop-user-workers-staging \
  --compatibility-date 2026-08-11 \
  --no-bundle
```

The dispatcher resolves public slugs through its `APP_ROUTES` KV binding. The
durable control worker publishes a slug only after compute registration
succeeds. During archival it deletes the customer Worker and leaves the slug
pointer until another App publishes that slug, then archives compute state.
KV has no conditional delete, so old cleanup cannot safely remove a slug that
may already belong to a new App. An unused archived slug resolves to a missing
Worker and returns `app_not_found` until reused. The
customer artifact itself owns explicit Workspace selection through the SDK;
the dispatcher and broker never infer a default.

App variables and secrets are not managed with Wrangler. The Cantelop control
plane patches the User Worker's complete binding set through the Workers for
Platforms API whenever an App configuration mutation succeeds. Normal
variables become `plain_text` bindings and write-only secrets become
`secret_text` bindings under the same names used in Sandbox VMs.

Deploy the dispatcher last through `platform/cloudflare/deploy-workers.sh`; do
not deploy its checked-in config directly because the helper supplies the KV
resource ID. Its checked-in route is `*.cantelop.dev/*`; the injected KV binding
resolves slugs to stable App IDs, and its dispatch namespace binding attaches
`cantelop-outbound` with `CANTELOP_CONTEXT`. The dispatcher fails closed for
missing, malformed, or unavailable registry entries. Terraform owns more-specific
no-Worker routes that protect the existing Hetzner router and infrastructure
hostnames from this wildcard. The configs assume all platform Workers live in the
same Cloudflare account.

The Wrangler files do not create the Tunnel, its DNS record, the Access
application, or its Service Auth policy. Those infrastructure resources and
the matching Hetzner secret must exist before live qualification.
