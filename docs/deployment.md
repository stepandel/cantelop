# Release and deployment runbook

For compatible software updates to an already provisioned host, use
[local component shipping](./local-shipping.md). That path selects Fire Fuse,
builder, or storage explicitly and qualifies only the selected component.
Use this coordinated procedure for protocol cutovers, incompatible shared
contracts, state migrations, or changes to host configuration and base images.

This is the canonical operator entry point for a coordinated Cantelop release.
It connects the checked-in release automation, the Message-protocol rollout,
and the Hetzner host playbooks. Component documents remain authoritative for
their individual contracts; this runbook owns their order and the evidence
required to move between them.

For a hard Message-protocol cutover, Fire Fuse, gateway,
asynchronous dispatcher, observer, control worker, console, Cloudflare edge,
SDK-bearing application Builds, and their schemas must be treated as one
release cohort. SDK 1.0 is the only supported SDK; selectable and rollback
Builds must use stable SDK 1.0. Never use an Executor-era Build as a compatibility
fallback.

The sole deployed environment is currently `staging`, and it serves live users.
Vercel's `Production` environment label refers to that live staging deployment.
Use the staging inventory throughout; do not create a second environment for
this rollout. Feature-specific sequencing is documented in
[`workspace-databases.md`](./workspace-databases.md#release-qualification).

## Automation map

The map includes the checked-in GitHub Actions workflows and the console's
external Vercel GitHub integration. These release and deployment checks do not
replace the local release qualification gate.

| Repository | Workflow | Trigger | Responsibility |
| --- | --- | --- | --- |
| `stepandel/cantelop-sdk` | [`Publish SDK`](https://github.com/stepandel/cantelop-sdk/blob/main/.github/workflows/publish.yml) | Manual, signed `sdk-v<version>` tag and exact version input | Installs the locked dependency graph; runs SDK checks, tests, examples, and packed-package qualification; publishes prereleases to npm `next` and stable versions to `latest` using npm trusted publishing. |
| `stepandel/cantelop-internal` | [`Publish Cantelop CLI`](../.github/workflows/publish-cli.yml) | A `v*` tag or manual selection of an existing tag | Requires a signed tag reachable from `main`; rebuilds and tests the CLI; signs and notarizes macOS binaries; verifies the release; publishes GitHub release assets; then calls the Homebrew workflow. |
| `stepandel/cantelop-internal` | [`Publish Homebrew tap`](../.github/workflows/publish-homebrew.yml) | Called by the CLI workflow, a published release, or manual selection of an existing tag | Verifies the private release evidence and tag identity, mirrors release assets into `stepandel/homebrew-tap`, and updates its Homebrew formula and checksum-pinned curl installer. |
| `stepandel/cantelop-internal` | [`Deploy Cloudflare`](../.github/workflows/deploy-cloudflare.yml) | Manual, from `main`, protected `cloudflare` environment | Tests and builds the broker, outbound Worker, and dispatcher; deploys them in dependency order; then verifies the deployed dispatcher and the public unknown-App response. It does not run Terraform or deploy Hetzner services. |
| `stepandel/cantelop-internal` | Cantelop Console through the Vercel GitHub integration | Git pushes according to the Vercel project branch policy | Uses `console` as the project Root Directory, installs the Bun lockfile, migrates the control and observability PostgreSQL schemas, and builds the Next.js console. Both schemas reuse the control connection unless an observability override is configured. Non-production branches migrate an isolated preview database when its control URL is configured, or skip both migrations; the configured production branch requires the control database and deploys the production console. It does not deploy the control worker. |

The Vercel deployment is CI/CD even though it is not represented by a file in
`.github/workflows`. Its project and GitHub integration settings are an
external deployment boundary described in
[`dashboard.md`](./dashboard.md#vercel-project). A successful Vercel build
proves that the console revision builds in Vercel; it is not evidence that the
full repository suite, database migrations, host deployment, or Message canary
passed. Fire Fuse and the other Hetzner services are deployed only through the
approval-gated Ansible procedure below.

The public Contributor checks workflow runs `make check`, a secret scan, and
mocked deployment configuration checks on pull requests without deployment
credentials. Record `make qualify-release` independently: contributor CI and a
Vercel preview do not qualify a live infrastructure release.

The workflows can be dispatched from GitHub or with equivalent commands:

```sh
sdk_version=SDK_VERSION
gh workflow run publish.yml \
  --repo stepandel/cantelop-sdk \
  --ref "sdk-v${sdk_version}" \
  -f version="${sdk_version}"

gh workflow run deploy-cloudflare.yml \
  --repo stepandel/cantelop-internal \
  --ref main
```

Do not dispatch a publishing or deployment workflow merely because a candidate
exists. Publication credentials, deployment credentials, and the required
environment approvals are separate authorization boundaries.

## 1. Select and qualify one cohort

Start with clean reviewed `main` branches in this repository and
`../cantelop-sdk`. Record:

- both full source commits;
- the SDK version and published package integrity;
- the Cantelop CLI version when the release includes a customer CLI;
- the Fire Fuse deployment revision and every host-binary SHA-256;
- every selectable Build ID, rootfs SHA-256, process-contract SHA-256, and the
  SDK version installed in that rootfs;
- the console revision, control-worker checksum, and Cloudflare revision;
- the Fire Fuse schema and PostgreSQL migration set; and
- the operator, approver, maintenance window, rollback owner, and evidence
  directory.

Run the pre-production gate before performing external operations:

```sh
make qualify-release
```

Select exactly one newly created `dist/release-qualification/RUN` directory
produced from the final reviewed commits. Before any production operation:

1. require both commits in `evidence.json` to be reviewed and present on their
   intended remotes;
2. recompute `artifacts/SHA256SUMS` and require an exact match;
3. require the package versions to match the reviewed SDK package and CLI
   version files;
4. require every recorded check log to end successfully;
5. require the Fire Fuse deployment revision, archive, state schema, source
   commit, and checksum in evidence to match the candidate selected for host
   rollout; and
6. record the operator, approval, maintenance window, and rollback owner.

Do not rebuild an artifact after approval; any source, identity, schema, or
artifact change creates a new candidate and requires a new qualification run.
See [`release-candidates.md`](./release-candidates.md) for what qualification
covers.

The public CLI version is defined independently from the SDK package version.
Fire Fuse and the other host binaries are internal deployment artifacts
identified by deployment revision, full source commit, and SHA-256, not by the
CLI's semantic version. API, state, and manifest schemas remain explicitly
versioned compatibility contracts. Package publication, signing, tag pushes,
release uploads, database changes, and infrastructure deployment are
independent failure domains: stop between them when verification does not
exactly match the approved evidence, and never use the local qualification
script as a production deploy command.

## 2. Publish the SDK and attest application Builds

The SDK package must exist in the public registry before constructing the
selectable canary rootfs. Dispatch the SDK workflow with the exact reviewed
version, then verify the registry result from a clean temporary consumer. The
workflow checks package contents before publication, but the operator must
record the workflow run URL, npm integrity, published tag, and source commit in
the cohort evidence.

Update the canonical starter to the published registry version and committed
lockfile, then run:

```sh
make check-starter
```

Build each application rootfs from that same locked SDK dependency graph.
Attest the resulting Build identity before adding it to the Fire Fuse image
catalog. A Build whose SDK identity cannot be proven is not selectable during
the cutover.

If the cohort includes a customer CLI release, create and push the separately
reviewed signed tag, allow `Publish Cantelop CLI` to complete, and require its
called Homebrew job to complete. CLI publication is not the mechanism that
deploys Fire Fuse or Cloudflare.

## 3. Prepare the deployment controller

The Ansible workflow is the sole host deployment path. Work from the reviewed
Cantelop source and copy
`platform/ansible/inventory/staging/hosts.example.yml` to the ignored
`hosts.yml`. Use absolute controller paths and exact lowercase SHA-256 values
for every artifact. Prefer extracting the qualified archive into one
revision-named, root-owned controller directory so every host-binary variable
points into the same immutable cohort.

The encrypted Vault must be the source of truth for host credentials. In
addition to the storage and control-worker values documented by Ansible, verify
that the deployment can supply distinct values for:

- the Fire Fuse control token and configuration-encryption key;
- the gateway edge-ingress token;
- the observer read token and Session-mutation token;
- the Cloudflare Tunnel token; and
- the control-worker PostgreSQL, artifact-store, and Cloudflare credentials.

Do not copy secret values into command history, Git, release evidence, or
service logs. A credential already installed on the host may be validated
during recovery, but it should not silently replace the encrypted deployment
source of truth.

Use the same Vault arguments on every playbook invocation. The examples below
show `--ask-vault-pass`; a protected password file or external secret provider
is an equivalent operator choice.

## 4. Prestage the qualified runtime before maintenance

After the host has been provisioned, preview, apply, and repeat
`stage-runtime.yml` against the immutable qualified artifacts. Staging verifies
the existing encryption key and writes only the isolated staged release
directory. It does not run `host_base`, replace active credentials, restart
services, or change the live cohort. A newly provisioned host must run
`compute.yml --tags base` and `playbooks/compute/prepare-service.yml` while
Fire Fuse is inactive before it can be staged. Record the staged manifest and
checksums. If the candidate changes, repeat qualification and staging before
entering maintenance.

```sh
cd platform/ansible
ansible-playbook --inventory inventory/staging/hosts.yml \
  --ask-vault-pass --check --diff playbooks/compute/stage-runtime.yml
ansible-playbook --inventory inventory/staging/hosts.yml \
  --ask-vault-pass playbooks/compute/stage-runtime.yml
ansible-playbook --inventory inventory/staging/hosts.yml \
  --ask-vault-pass playbooks/compute/stage-runtime.yml
ansible-playbook --inventory inventory/staging/hosts.yml \
  --ask-vault-pass --check --diff playbooks/builder/control-worker.yml
```

The staged repeat must report `changed=0` before the maintenance window
starts. Use `ANSIBLE_CALLBACKS_ENABLED=ansible.posix.profile_tasks` on apply
commands to record the slowest tasks. Wrap timed commands with
`scripts/measure-release-step.py` and put its JSONL file in the private rollout
evidence directory. It records timestamps, elapsed seconds, and exit codes
without recording command arguments or secrets. For example, from
`platform/ansible`:

```sh
ANSIBLE_CALLBACKS_ENABLED=ansible.posix.profile_tasks \
  python3 ../../scripts/measure-release-step.py \
  --evidence /private/rollout-evidence/timings.jsonl \
  --step stage-apply -- \
  ansible-playbook --inventory inventory/staging/hosts.yml \
  --ask-vault-pass playbooks/compute/stage-runtime.yml
```

Measure the maintenance window from ingress fencing through successful public
smoke. The 20-minute target remains a rehearsal goal; retain every canary and
recovery gate regardless of elapsed time.

## 5. Enter the maintenance boundary

Before changing the cohort:

1. Stop the public Cloudflare Tunnel connector so new requests cannot reach the
   host.
2. Drain accepted transport and application work, and record anything that
   does not drain. Require `quiescent` or `unavailable` transport executions and
   no active application work or compute lease; intake settlement alone does
   not prove drain.
3. Require zero starting, running, or stopping Sandboxes unless the approved
   procedure explicitly owns a canary.
4. Capture service status, readiness, schema versions, database integrity, and
   the current artifact checksums.
5. Disable stopped gateway, dispatcher, and observer units if they belong to
   the old cohort. `activate-runtime.yml` restores enabled local dependants
   after Fire Fuse becomes ready; leaving incompatible old units enabled can
   restart them too early. The control worker runs on the control/build VM and
   is deployed separately.

Public ingress remains stopped through host deployment and recovery testing.
Do not interpret an enabled but inactive Tunnel unit as restored ingress.

## 6. Promote Fire Fuse

With Fire Fuse stopped and disabled, create a unique retained rollback snapshot:

```sh
export CANTELOP_FIRE_FUSE_PREPARE_UPGRADE='PREPARE FIRE-FUSE PRODUCTION UPGRADE'
export CANTELOP_FIRE_FUSE_UPGRADE_ID='UTC_TIMESTAMP-DEPLOYMENT_REVISION'

ansible-playbook --inventory inventory/staging/hosts.yml \
  --ask-vault-pass playbooks/compute/prepare-runtime-upgrade.yml
```

Record the upgrade ID, source schema, and retained database SHA-256. Keep the
upgrade ID set while promoting so the role proves that the retained database
matches the live pre-migration database:

```sh
ansible-playbook --inventory inventory/staging/hosts.yml \
  --ask-vault-pass playbooks/compute/promote-runtime.yml

unset CANTELOP_FIRE_FUSE_PREPARE_UPGRADE CANTELOP_FIRE_FUSE_UPGRADE_ID
```

Promotion must leave Fire Fuse stopped, validate the expected schema, and
retain the rollback snapshot. Activate only after reviewing promotion:

```sh
ansible-playbook --inventory inventory/staging/hosts.yml \
  --ask-vault-pass playbooks/compute/activate-runtime.yml
```

Require loopback `/readyz`, the expected state schema, database integrity, and
an active/enabled Fire Fuse unit before installing dependants.

## 7. Deploy the dependent host cohort

Apply each role in dependency order. Use `--check --diff` where the role
supports check mode, apply normally, then repeat normally and require zero
changes. `playbooks/builder/control-worker.yml` no longer runs
`control_builder`. Provision the builder with
`playbooks/builder/prepare-control-builder.yml` before the release. If this
cohort changes the materializer, run that playbook after Fire Fuse activation
and before the control worker unless compatibility with the serving cohort
has been established. `builder.yml` remains the full host convergence path.

```sh
ansible-playbook --inventory inventory/staging/hosts.yml \
  --ask-vault-pass --check --diff playbooks/compute/workspace-gateway.yml
ansible-playbook --inventory inventory/staging/hosts.yml \
  --ask-vault-pass playbooks/compute/workspace-gateway.yml

ansible-playbook --inventory inventory/staging/hosts.yml \
  --ask-vault-pass --check --diff playbooks/compute/gateway.yml
ansible-playbook --inventory inventory/staging/hosts.yml \
  --ask-vault-pass playbooks/compute/gateway.yml

ansible-playbook --inventory inventory/staging/hosts.yml \
  --ask-vault-pass --check --diff playbooks/compute/observer.yml
ansible-playbook --inventory inventory/staging/hosts.yml \
  --ask-vault-pass playbooks/compute/observer.yml

ansible-playbook --inventory inventory/staging/hosts.yml \
  --ask-vault-pass playbooks/builder/control-worker.yml
```

`gateway.yml` owns two separate systemd processes: the request gateway and the
single active asynchronous dispatcher. Require both units to be active and the
gateway to be ready. The observer playbook must qualify authenticated snapshot
and Message telemetry through loopback and public TLS. The control-worker
playbook applies serialized PostgreSQL migrations before restarting its worker
and qualifies authenticated private compute ingress.

The workspace-gateway run must prove that rpcbind remains masked and inactive,
port 111 has no listener, and its IPv4/IPv6 firewall rules are present before
the release continues. After the first successful application, rerun all four
playbooks and record their `changed=0` recaps. Keep the Tunnel connector
inactive.

Time each playbook and record the `ansible.posix.profile_tasks` slowest-task
summary. The control-worker repeat verifies the installed migration bundle
without retransferring a healthy one. A nonzero change recap or checksum
mismatch blocks ingress restoration.

## 8. Deploy external members of the cohort

Require the Vercel GitHub integration to build the exact reviewed console
commit. Review its preview deployment first, then require the same commit to be
the production deployment through the configured production branch. Record
the Git commit, Vercel deployment identity, build result, production alias, and
environment configuration identity. The current Vercel build script runs
control and observability migrations whenever the control database URL is
configured. The control-worker host deployment runs the serialized migration
command as well. Establish the maintenance boundary before either deployment
starts, including a Git merge that triggers Vercel. Verify both migration
results against the same reviewed migration set.
Use the integration's configured `console` root. If a manual deployment is
required, export a clean tree from the reviewed commit and verify the upload
contains only the intended project files before starting Vercel; do not upload
the controller workspace, release archives, or `dist` evidence.

Dispatch `Deploy Cloudflare` from the exact reviewed `main` commit and record
its workflow run and deployed Worker versions. The workflow deploys only the
shared Worker chain; it does not apply Terraform, change the Hetzner connector,
or validate the SDK-bearing Sandbox lifecycle.

If Cloudflare resources or credentials changed, apply the separately reviewed
Terraform plan first, transfer the outputs into their secret boundaries, and
then run the Worker workflow. Never infer Terraform authorization from approval
of the Worker workflow.

Do not restore the Tunnel merely because external deployment succeeded.

## 9. Run the deployed Message canary

Use a dedicated canary App and Workspace whose selectable Build is attested to
the published SDK. Keep the connector inactive while preparing the canary. For
the complete lifecycle case, open a controlled Tunnel window, send only the
authorized canary request through the public SDK path, and stop the connector
again after the result is terminal. If ordinary callers cannot be excluded
during that window, the environment needs an explicit maintenance gate before
the public canary is safe. Run every dispatcher interruption and lost-generation
case with the connector stopped.

Follow the SDK 1.0
[application-owned mailbox acceptance gates](./message-rollout.md#application-owned-mailbox-acceptance)
and preserve sanitized evidence for:

1. `accepted -> handling -> settled -> quiescent`, correlated across edge,
   gateway, Fire Fuse, dispatcher, observer, concrete Session, attempt, and
   mailbox generation;
2. Sandbox retention while SDK mailbox work remains active and termination
   only after quiescence and keep-alive expiry;
3. dispatcher interruption during `handling`, producing a safe requeue and a
   later successful attempt;
4. dispatcher interruption after `settled`, resuming quiescence observation
   without delivering the Message to the mailbox again; and
5. loss of the settled Sandbox generation, producing `unavailable` and
   releasing the work lease.

Finish with zero nonterminal Messages, no leaked Sandbox, all private services
healthy, and no Message payload in transition events or logs. A timeout,
unexpected redelivery, premature Sandbox release, missing correlation field,
or mixed artifact identity fails the rollout.

## 10. Restore ingress and close the release

Only after every required canary result passes:

1. reconcile or start the checksum-pinned Tunnel connector with the matching
   edge credential;
2. require connected readiness on `127.0.0.1:20241`;
3. run a public SDK smoke request through the Cloudflare edge;
4. recheck Fire Fuse, gateway, dispatcher, observer, control worker, nginx,
   Tunnel, schema, integrity, and nonterminal Message count;
5. attach workflow URLs, service status, transition evidence, checksums, schema
   versions, migration results, and canary identifiers to the rollout record;
   and
6. retain the Fire Fuse snapshot until the approved rollback window closes.

The release is not complete when the binaries are merely active. It completes
when the public path, SDK-bearing Sandbox, mailbox quiescence, recovery cases,
and final evidence all pass as one cohort.

## Rollback

Keep ingress stopped while choosing fix-forward or rollback. Rollback is
cohort-wide; never pair a restored database with Message-era services of a
different identity.

To restore the retained Fire Fuse state, stop and disable Fire Fuse and its
dependants, select the recorded upgrade ID, and run the guarded rollback:

```sh
export CANTELOP_FIRE_FUSE_ROLLBACK='RESTORE FIRE-FUSE PRODUCTION ROLLBACK'
export CANTELOP_FIRE_FUSE_UPGRADE_ID='RETAINED_UPGRADE_ID'

ansible-playbook --inventory inventory/staging/hosts.yml \
  --ask-vault-pass playbooks/compute/rollback-runtime.yml

unset CANTELOP_FIRE_FUSE_ROLLBACK CANTELOP_FIRE_FUSE_UPGRADE_ID
```

The rollback role deliberately leaves Fire Fuse stopped. Restore and verify the
matching gateway, dispatcher, observer, worker, callers, SDK-bearing Builds,
and external revisions before explicit activation. Reconcile any Messages
accepted after the retained snapshot; they must not be silently discarded.

## Inspecting an App deployment

The App's Builds tab shows separate API and Session runtime activity. Pending
and failed releases open their deployment details automatically. Each component
reports its attempt count, last operation activity, and (while queued) the time
it becomes eligible to run. Eligibility is not a guaranteed start time: a worker
must claim the operation. Zero attempts means no worker has claimed it yet;
repeated attempts indicate retries or continued materialization polling, not
necessarily an error. Last activity is a durable operation update, not a heartbeat.

`cantelop deploy` prints component activity changes even when the release stays
in `provisioning`. `cantelop releases --json` exposes the same component
`attempts`, `updated_at`, and `next_attempt_at` fields for programmatic inspection.
The deployment GET and list APIs also include these additive fields. Older
servers may omit them. Terminal error codes appear in deployment details;
provider logs, credentials, and internal operation output are not exposed.


### Release recovery rollout

Deploy the Fire Fuse ingress and build worker with cancellation support before
rolling out the console/control worker and migrations `0006_release_recovery` and `0007_deployment_progress`.
The recovery worker requires an acknowledged provider cancellation; a missing
DELETE route on an older ingress leaves the release cancelling and retries.
Deploy the updated CLI to accept cancelling/cancelled release statuses.

The console and tenant API accept POST on
`/v1/apps/{app_id}/deployments/{release_id}/cancel` and `/restart` (202 while
cancelling). Restart uses retained artifacts and fresh operation IDs. Cancellation
is not rollback: already completed API/runtime provider changes are preserved.

Recovery waits for App registration and queues a fresh registration from the
current App record if the previous operation failed. Successful registrations
are retained; replaying their initial empty-Build shape against a deployed App
would conflict with its active Build. A restart also projects the current
configuration revision before creating the replacement release. Repair operation
keys are scoped to the recovery (and configuration revision), so retries do not
duplicate work or erase the original failure history. Cancel-only does not
require configuration synchronization.

The cancellation operation's `progress` records `recovery_registration_pending`,
`recovery_registration_failed`, `recovery_configuration_pending`, or
`recovery_configuration_failed` while prerequisites block progress. A failed
repair requires operator intervention; repeated polling does not create an
unbounded stream of repair operations. Provider acknowledgment remains required
before clearing `cancelling`; never clear the release status manually to bypass
that fence. A generic provider 404 is not proof of successful cancellation.

Component `progress` distinguishes provider receiving/queued/processing states
from retry reasons such as unreachable ingress, HTTP conflicts, and rate limits.
These are sanitized codes; provider response bodies and signed URLs stay private.
Control queues poll independently so slow materialization calls do not hold up
cancellation requests.

### Deployment logs

The console's **Builds → (expand a build) → Build & deployment logs** panel
records timestamped API and Session runtime deployment activity for each release.
It refreshes while a deployment is pending and supports text search, warning/error
filters, following new lines, copying, and downloading. Retries, provider status
codes, cancellation, failures, and release activation are retained in PostgreSQL
and survive worker restarts. Only the most recent 2,000 lines are returned; the
panel explicitly indicates truncation and exports the same bounded tail.

Apply control database migration `0008_deployment_logs` before deploying this
console/worker version. Database triggers capture lifecycle transitions atomically
with their state changes, including stale-worker recovery. The Drizzle schema
tracks the table; the migration also owns the trigger functions. Logs are retained
with their release and deleted if that release is deleted. Read access is scoped
to the authenticated release owner and App. Operation inputs, provider response
bodies, signed artifact URLs, and secrets are not copied into these logs.

Recording starts when the migration is applied; past transitions cannot be
reconstructed. These are server-side deployment logs. Source compilation and
Docker builds currently run locally in `cantelop deploy`; their stdout/stderr and
failures before a release is created are not uploaded to this panel.
