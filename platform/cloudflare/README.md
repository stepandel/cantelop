# Cloudflare edge infrastructure

The defaults preserve the hosted staging identities below. Independent
installations set `app_domain`, `zone_name`, `resource_prefix` and `environment`
from the [self-hosting profile](../../docs/self-hosting.md). This Terraform root
owns the resources around the Worker request chain:

- the remotely managed `cantelop-edge-staging` Tunnel;
- the untrusted `cantelop-user-workers-staging` Workers for Platforms dispatch
  namespace;
- the protected `cantelop-app-routes-staging` KV namespace used by the public
  dispatcher;
- `edge-origin.cantelop.dev` routed to that Tunnel;
- an Access self-hosted application with one Service Auth policy and token;
- the originless proxied `*.cantelop.dev` DNS record required by the
  dispatcher Worker route;
- explicit no-Worker routes protecting console, control, status, and
  infrastructure hostnames; and
- the random 256-bit credential shared only by the origin broker and Hetzner
  gateway.

It does not deploy Worker source or install `cloudflared` on Hetzner. Worker
deployment uses Wrangler independently of Terraform; the adjacent Ansible role
installs the connector.

## Authentication and state

For local Terraform operations, set `CLOUDFLARE_API_TOKEN` to a scoped token
with read/write access for Tunnel
connectors and configuration, DNS, Access applications and policies, and Access
service tokens. Terraform additionally needs Workers Scripts, Workers Routes,
and Workers KV Storage write access to manage the user Worker namespace,
dispatcher exclusions, and route registry.
The GitHub deployment uses a separate token scoped only to the Workers Scripts,
Workers Routes, and Workers KV Storage permissions needed by Wrangler.

The external control worker uses a separate scoped token with Workers Scripts
and Workers KV Storage write access to upload SDK artifacts, project App
configuration, and manage public App routes. That token belongs only in the
console/worker deployment secret boundary; it must not be copied to Fire Fuse,
customer artifacts, Worker source, Terraform variables, or Terraform state.

Copy `terraform.tfvars.example` to the ignored `terraform.tfvars`, insert the
account and `cantelop.dev` zone IDs, and keep it mode `0600`.

Terraform state contains the Tunnel token, Access client secret, and edge
ingress credential. Local state is ignored by Git but is still plaintext on
disk. This staging workflow requires a private encrypted workstation; move the
state to an encrypted remote backend before it becomes shared or production
infrastructure.

## Provision the Cloudflare resources

The staging route namespace predates Terraform ownership. Before the first
plan with this revision, adopt it into the existing state exactly once:

```sh
terraform import cloudflare_workers_kv_namespace.app_routes \
  '<cloudflare-account-id>/a39774ebb6414f5d9a7caaf1e2f5df7b'
```

Import preserves its existing route entries. Do not apply a plan that proposes
creating a replacement namespace.

```sh
terraform init
terraform fmt -check -recursive
terraform validate
terraform plan -out=cantelop-edge.tfplan
terraform apply cantelop-edge.tfplan
```

Review the plan before applying it. The origin must be exactly
`edge-origin.<app_domain>` and the wildcard exactly `*.<app_domain>` within
`zone_name`; independent hostname overrides cannot widen that boundary.
Credential-free validation uses `terraform init -backend=false` followed by
`terraform test`, which mocks both providers and never applies infrastructure.

The broad dispatcher route is safe only while the checked-in no-Worker routes
exist. Cloudflare selects the more-specific exclusions for
`control.apps.cantelop.dev`, `status.apps.cantelop.dev`, `console.cantelop.dev`,
`edge-origin.cantelop.dev`, and `www.cantelop.dev`, preserving those existing
routes even if their DNS proxy status changes later. Control and status are
explicit rather than one `*.apps` exclusion so the first apply does not collide
with the dispatcher's pre-migration `*.apps.cantelop.dev/*` route.

Cloudflare's provider cannot delete a remotely managed Tunnel configuration
through Terraform. Destroying this staging root therefore requires manually
removing any residual Tunnel configuration in Cloudflare after the Terraform
destroy completes. The App route namespace is also protected with
`prevent_destroy`; remove that protection deliberately before a full teardown,
after deciding whether its route entries need to be retained.

## Deploy the Workers

The helper requires `jq`, Bun, and Wrangler. It reads deployment configuration
from these environment variables rather than from Terraform state:

- `APP_ROUTES_KV_NAMESPACE_ID`;
- `ORIGIN_URL`;
- `EDGE_INGRESS_TOKEN`;
- `ORIGIN_ACCESS_CLIENT_ID`; and
- `ORIGIN_ACCESS_CLIENT_SECRET`.
- `RELEASE_GATE_TOKEN`, a separate 64-character lowercase hex control credential.

For a rendered operator profile, set `CANTELOP_DEPLOYMENT_DIR` to its absolute
output directory; otherwise the helper uses the checked-in hosted configs.
Keep the same profile in Terraform, gateway, control worker and console.

Run it from this directory:

```sh
./deploy-workers.sh
```

The helper deploys the shared Cloudflare edge in dependency order: broker,
outbound Worker, Tail Worker, then dispatcher. Its temporary dispatcher configuration binds
the Terraform-owned route namespace without hardcoding provider resource IDs
in Worker source. Customer Workers and App-route entries are deployed by their
application deployment path, not by this edge workflow.

The dispatcher upload omits `tail_consumers`, then applies that configuration
through Cloudflare's non-versioned script settings API and reads it back.
On the deployment account, script uploads containing the tail attachment return
error 100150 despite an active Workers for Platforms subscription; the settings
API accepts the attachment. A missing or mismatched readback fails deployment.
Keep public ingress paused during a coordinated rollout until this step and
release reconciliation succeed. This preserves the existing Tail Worker logs.

### GitHub deployment

`.github/workflows/deploy-cloudflare.yml` exposes the live Cloudflare deployment
as a manual, `cloudflare` environment-gated workflow. It runs on a GitHub-hosted
Ubuntu runner, deploys only the selected `main` revision, and never invokes
Terraform or the Fire Fuse workflow.

Configure these environment values before enabling it:

- secret `CLOUDFLARE_API_TOKEN`, scoped to the required Worker operations;
- secrets `EDGE_INGRESS_TOKEN`, `ORIGIN_ACCESS_CLIENT_ID`, and
  `ORIGIN_ACCESS_CLIENT_SECRET`, plus `RELEASE_GATE_TOKEN`;
- variables `CLOUDFLARE_ACCOUNT_ID`, `APP_ROUTES_KV_NAMESPACE_ID`, and
  `ORIGIN_URL`.

`RELEASE_GATE_TOKEN` must be the same 64-character hex value installed as
`vault_release_gate_token` for the Console control worker. Follow the rollout
order in `docs/release-activation-security-plan.md`; the new dispatcher starts
existing App gates closed until Console reconciles them.

Populate `APP_ROUTES_KV_NAMESPACE_ID`, `ORIGIN_URL`, and the three runtime
credential secrets from the current local Terraform outputs after an
infrastructure apply. Read `CLOUDFLARE_ACCOUNT_ID` from the local Terraform
input. Terraform state and variables remain local to the operator machine and
are never available to the GitHub workflow. Protect the GitHub environment with
required reviewers and rotate consumer secrets deliberately whenever the
corresponding Terraform-managed credentials change.

## Install the Hetzner connector

Store these sensitive Terraform outputs in the operator's ignored inventory or
Ansible Vault boundary:

```text
cloudflare_edge_tunnel_token       <- terraform output tunnel_token
fire_fuse_gateway_edge_token       <- terraform output edge_ingress_token
```

Also supply a checksum-pinned Linux AMD64 `cloudflared` artifact through
`cloudflare_edge_tunnel_binary_artifact` and
`cloudflare_edge_tunnel_binary_checksum`. Reconcile the gateway first so its
matching edge token and edge-aware binary are live, then install the outbound
connector:

```sh
ansible-playbook --inventory inventory/staging/hosts.yml playbooks/compute/gateway.yml
ansible-playbook --inventory inventory/staging/hosts.yml playbooks/compute/edge-tunnel.yml
```

The connector reads its token from a root-owned file rather than its command
line and exposes readiness only on `127.0.0.1:20241`.
