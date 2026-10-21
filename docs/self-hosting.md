# Self-hosting installation candidate

This procedure is being prepared for the self-hosted alpha. It has not yet
passed a fresh three-host installation. The repository remains a public source
preview. Do not treat a successful build, configuration preflight, mocked
Terraform plan, or hosted deployment as installation qualification.

The candidate reuses the platform's provisioning and release checkpoints.
The [Ansible guide](../platform/ansible/README.md),
[Cloudflare guide](../platform/cloudflare/README.md), and
[storage qualification](../storage/juicefs/README.md#qualification-and-rollout)
describe their detailed contracts. References to the maintainer's staging
inventory in those guides are examples; use your own private inventory and
accounts for this installation.

## Reference topology and prerequisites

| Component | Candidate specification |
| --- | --- |
| Compute | One dedicated Ubuntu 24.04 x86_64 host, hardware virtualization, `/dev/kvm`, at least 64 GB RAM and 80 GB free root storage; separate persistent SSD capacity for application images/state |
| Storage | Separate Ubuntu 24.04 x86_64 host, at least four vCPUs, 8 GB RAM and 80 GB free storage; Redis metadata, MinIO objects and JuiceFS; size durable storage separately |
| Builder | Separate Ubuntu 24.04 x86_64 host, at least four vCPUs, 8 GB RAM and 80 GB free storage; Linux amd64 Docker engine and build materializer/control worker |
| Control state | Operator-owned PostgreSQL, reachable from console and builder, with verified TLS and backup/restore access |
| Edge | Operator-owned Cloudflare account and active zone, Workers for Platforms, Workers KV, R2, Tunnel and Access service authentication |
| Workspace databases | Operator-owned Turso organization/group and platform API token |
| Console | Separate Vercel project or compatible Next.js server with HTTPS, GitHub and Google OAuth applications |
| Controller | Git, Python 3.12+, Go at least the `go.mod` version, exact Bun from `console/package.json`, Terraform 1.8+, Ansible, OpenSSH, OpenSSL, jq, and a Linux Docker engine |

The hardware values are a qualification target, not measured production
capacity. Firecracker/jailer are pinned to 1.15.1 by the preparation script.
Guest kernel/rootfs hashes, snapshot dimensions and the SDK package revision
must be recorded with installation evidence. ARM64 compute and Docker-only
runtime hosting are outside this reference candidate.

Use distinct accounts/projects, a dedicated App subdomain, and fresh
credentials. Budget for the dedicated server, two VMs, PostgreSQL, object
storage/backups, console hosting, Cloudflare products, Turso, traffic and DNS.
Product availability and limits must be verified in the operator's accounts
before applying infrastructure. The preflight does not call provider APIs or
verify billing entitlements.

## 1. Select public sources and render identities

Clone the public repository and select a reviewed full commit. Keep the
installation inventory and credentials outside that checkout. Do not copy
the maintainer's `.env`, Terraform state, caches, host addresses or Vault.

```sh
git clone https://github.com/stepandel/cantelop.git
cd cantelop
git checkout --detach REVIEWED_FULL_COMMIT
python3 -m venv /private/path/cantelop-tools
/private/path/cantelop-tools/bin/pip install -r scripts/requirements-test.txt
cp deployment.example.json /private/path/deployment.json
# Edit only non-secret identities in deployment.json.
python3 scripts/self-hosting.py render \
  --config /private/path/deployment.json --output /private/path/rendered
```

The output directory must be new. It contains generated Wrangler configs,
Terraform inputs, Ansible identity variables and a console environment
template. Rendering creates no resources or credentials. Keep the generated
directory outside Git; generated Worker entry points use absolute paths into
this checkout, so re-render after moving it. `console_origins`, when supplied,
replaces the hosted allowlist with exact HTTPS origins. It must include
`console_origin`; arbitrary forwarded hosts never become trusted.

The example uses `agents.example.net` within zone `example.net`. Set the same
profile on all services. The reserved gateway is `edge-origin.<app_domain>`,
release gate is `release-gate.<app_domain>`, and observer is
`status.apps.<app_domain>`. Terraform preserves infrastructure exclusions
under the App suffix. Host the console on the reserved `console.<app_domain>`
host or a separate domain outside the App suffix; another App slug would
collide with the wildcard dispatcher route.

The generated reference inventory uses two warm slots, at most eight VMs and
a 32 GiB aggregate guest memory cap. Tune these only after measuring this
installation; the checked-in hosted defaults have a larger warm pool.

```sh
cd console && bun install --frozen-lockfile && cd ..
cd edge/workers && bun install --frozen-lockfile && cd ../..
python3 scripts/self-hosting.py preflight --config /private/path/deployment.json
```

The JSON report includes only setting names and failure categories. Supplying
`--skip-tools` checks profile syntax only. Supplying `--env-file` also checks
literal environment assignments, required settings, selected formats and
credential separation. It never evaluates shell expressions. Neither mode
qualifies a live installation or checks that credentials authenticate.

## 2. Configure accounts, identities and secrets

Copy `rendered/console.env.example` to private secret storage and fill every
required value. Set `CANTELOP_SOURCE_REPOSITORY` to the public repository
containing your actual modified source. Supply its full corresponding
`CANTELOP_SOURCE_COMMIT`, or use Vercel's full `VERCEL_GIT_COMMIT_SHA` when
deploying that public repository directly. Verify `/source` after deployment.

| Settings | Required purpose and lifetime |
| --- | --- |
| `NEXTAUTH_SECRET`, both OAuth client ID/secret pairs, `ADMIN_EMAIL` | Console authentication; register `https://<console-host>/api/auth/callback/github` and `/api/auth/callback/google`; use the operator's verified administrator email |
| PostgreSQL URLs, `CANTELOP_DATABASE_SSL=verify-full` | Shared control and observability state; migrations need schema privileges; retain restore access |
| `CANTELOP_CONFIG_ENCRYPTION_KEY` | Base64url 32-byte AES key; preserve with database backups or encrypted configuration becomes unreadable |
| `CANTELOP_CONTROL_PRIVATE_KEY` | Base64url PKCS#8 DER Ed25519 private key; preserve the identity needed for issued control credentials |
| Service, gateway, observer, mutation, edge ingress, release gate and database capability tokens | Dedicated 32-byte lowercase hex secrets; map each to its consumer using the role's Vault variables; never reuse them across services |
| Cloudflare account/zone/KV IDs, private deployment/API tokens, separate observability token | Deploy Workers, publish routes and ingest observations; use scoped account permissions |
| R2/S3 artifact credentials, storage Redis/MinIO credentials and WireGuard keys | Private image artifacts and persistent Workspace storage; keep durable storage keys/backups together |
| Turso organization/group/environment and platform token | Workspace database provisioning; preserve the organization and database restore material |
| Resend invitation and contact settings | Optional mail features; without configuration those sends are unavailable |

Generate each 32-byte hex secret separately with `openssl rand -hex 32` into a
private secret store. Do not paste it into Git or command logs. Generate the
AES key from 32 random bytes encoded as unpadded base64url; generate an Ed25519
key using OpenSSL, export PKCS#8 DER, then encode it as unpadded base64url.
Provisioning roles validate additional artifact, service and Vault contracts;
console preflight is not a replacement for those assertions.

Create a private inventory from
[`hosts.example.yml`](../platform/ansible/inventory/staging/hosts.example.yml).
Keep the three groups `fire_fuse_hosts`, `storage_hosts`, and
`control_builder_hosts`; replace all example hosts, keys, IDs and artifact pins.
Use Ansible Vault for credentials. Never disable SSH host-key checking.

Run the read-only host check after verifying SSH fingerprints:

```sh
cd platform/ansible
ansible-playbook -i /private/path/hosts.yml playbooks/self-hosting-preflight.yml
cd ../..
python3 scripts/self-hosting.py preflight --config /private/path/deployment.json \
  --env-file /private/path/console.env
```

The host check gathers facts and inspects `/dev/kvm`; it does not install
packages. It does not prove KVM can start a VM, provider entitlements, firewall
rules, storage durability or credentials. Those require later checkpoints.

## 3. Build one reproducible artifact cohort

From the selected checkout, run `make check`, then
`make compute-artifacts builder-artifacts`. Storage artifact construction must
run on Linux x86_64 with Docker: `make storage-artifacts`. Its preparation
scripts fetch public pinned upstream sources and verify their identities.
Record source commits and every SHA-256; transfer those exact artifacts and
absolute paths into the private inventory. The builder check
`bun scripts/check-control-worker-build.ts` executes its compiled Linux probe
in Docker. A controller without a Linux Docker engine cannot pass that gate.

Prepare the checksum-pinned kernel and guest rootfs using the
[Hetzner staging guide](../platform/hetzner/README.md), then the Ansible guest
and snapshot instructions. Image construction needs privileged Linux access;
it is not part of read-only preflight. Record the public SDK package version
and integrity used by the starter and every selectable Build. Existing examples
and older release prose may target different SDK revisions; resolve that cohort
and run the compatibility checks before declaring alpha support. Do not select
an adjacent local SDK checkout implicitly. If qualifying SDK source, clone its
public repository at an explicit reviewed commit and set
`CANTELOP_SDK_DIRECTORY` to it. The coordinated `make qualify-release` gate
currently additionally requires both source checkouts clean on `main`.

## 4. Provision in dependency order

Every command below this point changes operator infrastructure or application
state. Review the inputs and provider plan before running it. Use a new empty
Terraform state location for this installation; Terraform state contains
credentials. Do not point at the hosted state or run `terraform destroy` there.

1. In `platform/cloudflare`, initialize Terraform, supply your own account and
   zone IDs plus `rendered/deployment.auto.tfvars.json`, and inspect a saved
   plan. Apply that plan to create the namespace, KV, Tunnel, Access boundary
   and DNS. Store sensitive outputs privately; use the edge ingress credential
   only on gateway and origin broker. Configure the observer DNS/TLS certificate
   for `apps.<app_domain>` using an operator-owned DNS credential. Its files must
   remain under `/etc/letsencrypt/live/<observer-domain>/`.
2. Deploy `platform/ansible/storage.yml` with your private inventory and Vault.
   This establishes the compute/storage WireGuard peers, Redis, MinIO and
   JuiceFS. Configure the provider firewall through the existing Hetzner Robot
   role with your own durable SSH ranges; it replaces the complete provider
   ruleset, so inspect its read-only check first.
3. Deploy `compute.yml --tags base`, then the explicit compute release
   checkpoints: `stage-runtime.yml`, `prepare-service.yml`,
   `promote-runtime.yml`, `build-runtime-snapshot.yml`, `activate-runtime.yml`
   and `qualify-runtime.yml`, under `playbooks/compute/`. Follow the Ansible
   guide's first-install prerequisites and review each manifest/checksum.
   Re-run full `compute.yml` only after the runtime is active; gateway, observer
   and edge tunnel require it.
4. Deploy `builder.yml` after compute readiness. PostgreSQL migrations run
   through its pinned migration artifact before the control worker starts.
   Pass the generated identities with
   `-e @/private/path/rendered/ansible-deployment-vars.json` to **every** relevant
   Ansible invocation, in addition to your private inventory and Vault. Fill
   remaining worker inputs, including Turso settings, in that inventory.
5. Deploy the console from the selected public source in your own hosting
   project, Root Directory `console`, with the private environment settings.
   The Vercel build validates required production settings and migrates the
   PostgreSQL schemas before Next.js builds. A different host must execute the
   corresponding migration/build commands itself. Keep console and control
   worker on the same schema-compatible cohort.
6. Deploy the generated Workers with
   `CANTELOP_DEPLOYMENT_DIR=/private/path/rendered platform/cloudflare/deploy-workers.sh`
   after supplying its documented secret environment variables from a private
   store. The script deploys broker, outbound, tail and dispatcher in order;
   it uploads secrets and verifies tail settings. `OBSERVABILITY_INGEST_TOKEN`
   here is the **edge** ingestion credential. Set `APP_ROUTES_KV_NAMESPACE_ID`
   from the new KV output. Keep Workers deployment credentials outside public CI.

Do not use the hosted deployment workflows for an independent installation.
Use `cantelop login -console https://<console-host>` to select your custom control origin, then create
and deploy a starter through that connection. Verify its returned App URL uses
your App domain. Both OAuth callbacks, `/source`, observer authentication and
the unknown-App edge response must be checked before opening access.

## 5. Qualification, recovery and support boundary

Keep evidence with full component commits, artifact hashes, provider product
versions, OS/kernel, snapshot dimensions, SDK version/integrity and operator
date. Record pass/fail and sanitized outputs for each item:

- Fresh installation and migrations, first administrator and both OAuth providers.
- Custom-origin CLI connection; starter deployment; API, Session, Message and
  request operations; logs, variables and secrets.
- Workspace files and databases survive Session restart and host restart.
- Cold activation, release selection/rollback and admission failure behavior.
- Tenant isolation and NFS qualifications; unauthenticated observer access denied.
- Restore control PostgreSQL, Fire Fuse state, JuiceFS Redis/MinIO, Workspace
  databases and encryption/identity keys into the isolated installation.
- Teardown removes only the new installation; provider dashboards show no
  unintended remaining resources or charges. Destroying storage/state loses
  data; export and verify backups before deletion.

Use the [storage backup and restore playbooks](../platform/ansible/README.md)
and [state compatibility policy](../platform/ansible/README.md#initialize-and-validate-lifecycle-state) for their
explicit checkpoints. Back up before schema changes. Do not roll an older
binary back across an unsupported database schema. Compatible component
updates use [local shipping](./local-shipping.md); coordinated protocol/schema
updates use the [release procedure](./deployment.md) with your private inventory.

If preflight fails, fix the named setting or tool without enabling permissive
origins, disabling TLS verification or reusing credentials. KVM absence requires
a suitable host. A missing namespace/service binding usually indicates mixed
generated and hosted configuration; re-render one profile and compare names.
Keep logs and environment values private when requesting help.

An independent operator must reproduce this procedure before the project
publishes an alpha support matrix. Until then there is no claim of a qualified
self-hosted release, upgrade guarantee, or measured reference capacity.
