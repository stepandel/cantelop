# Open-source release plan

Status: source publication in progress, 2026-10-21. The maintainer selected a
fresh public `stepandel/cantelop` repository and renaming the existing private
repository to `stepandel/cantelop-internal`, with Vercel remaining private.
The [publication procedure](source-publication.md) supersedes the initial
single-repository/history defaults below. Self-hosted qualification remains pending.

## Outcome and release boundaries

Open Cantelop's platform implementation to inspection, contribution, and
independent operation while continuing to offer hosted Cantelop.

Deliver two milestones:

1. **Public source preview:** licensed source, reviewed publication contents,
   safe contribution workflows, reproducible contributor checks, and an honest
   statement of installation limitations.
2. **Self-hosted alpha:** a documented, independently qualified installation on
   the current architecture using the operator's own accounts and domain.

Source publication does not wait for a one-command installer or alternative
providers. The alpha supports one reference deployment before expanding it.

## Working decisions

These are recommendations to resolve before the affected release work lands.

| Decision | Proposed default | Decision owner |
| --- | --- | --- |
| Platform license | AGPL-3.0-only for Cantelop-authored platform code, with explicit exceptions documented by path | Maintainer |
| SDK | Preserve the separate SDK's existing MIT license | Maintainer |
| CLI and shared packages | Include in the platform license initially; assess any permissive exception against the complete import graph | Maintainer, with engineering evidence |
| Generated applications and examples | Explicit MIT grants for Cantelop-authored starter/template content; document the boundary between generated content and the CLI implementation | Maintainer |
| Contributions | Start with a documented Developer Certificate of Origin sign-off process; resolve any requirement for broader relicensing rights before accepting contributions | Maintainer |
| History | Start public history from a reviewed snapshot; retain existing history, issues, logs, and releases privately | Maintainer: selected |
| Public development | Public contributions in `stepandel/cantelop`; reviewed promotion to private `stepandel/cantelop-internal`, which retains Vercel and deployment authority | Maintainer: selected |
| First supported installation | Existing three-host Linux amd64 architecture, PostgreSQL, Cloudflare edge, Turso, and documented console hosting | Engineering |

The license map must account for CLI imports of runtime packages and source
strings in `clients/cli/cmd/cantelop/project_init_templates.go`. Directory names
alone do not establish a clean licensing boundary. Third-party code keeps its
own applicable license. The Cantelop name and logo need an explicit trademark
and asset policy.

AGPL permits commercial hosting; it is not a prohibition on competing services.
If permissive adoption becomes the primary goal, Apache-2.0 is the alternative
to decide on before publication. Review ownership, contributor agreements,
and the final license map with the person responsible for legal decisions.

## Evidence and limits of the initial assessment

At assessment commit `042fec76ed35fb62485008d3f428a4bae99cea40`:

- GitHub reported the platform repository private and without a license. The
  separate SDK repository was public and MIT licensed.
- An initial pattern scan covered roughly 1,800 tracked text files and found
  no confirmed live credential. It was not a dedicated full-history secret
  scan and did not inspect Actions logs, release assets, or issue contents.
- The local branch had 1,911 commits. Live infrastructure identifiers appear
  in [staging group variables](../platform/ansible/inventory/staging/group_vars/fire_fuse_hosts.yml).
  Historical rollout and benchmark records need individual publication review.
- GitHub's default workflow token was read-only. The `cloudflare` environment
  had a branch policy; GitHub's `Production` and `Preview` environments had no
  protection rules. This does not establish the safety or unsafety of the
  external Vercel integration, which still needs inspection.
- [Vercel builds](../console/scripts/vercel-build.ts) run database migrations
  when a control database is configured. Preview database isolation is a
  required deployment boundary, documented in [dashboard setup](dashboard.md#vercel-project).
- Terraform restricts domains to Cantelop's current names. The console has a
  fixed production-origin allowlist. Self-hosting requires coordinated changes
  to these policies, not only replacement values in example environment files.
- The [CLI](../scripts/build-cli-release.sh) and
  [Fire Fuse](../scripts/build-fire-fuse-release.sh) archive builders currently
  package binaries without a platform license or third-party notice bundle.
- Existing [release qualification](release-candidates.md) provides a substantial
  foundation. Its SDK checkout, branch, and version prerequisites need an
  explicit reproducible contributor path.

Recheck findings against the selected release commit. The existing
[security audit](security-audit.md) covers an earlier single-host scope; it is
not clearance for the current complete hosted platform.

## Milestone A: public source preview

### A1. Review the publication surface

Prepare a private disclosure report tied to the candidate commit and all refs
that would become public. Keep credential values and sensitive findings out of
PRs, public issues, and the public evidence bundle.

- Run a dedicated secret scanner across reachable branches and tags, including
  deleted files and renamed deployment directories. Triage matches rather than
  treating scanner silence as proof of safety.
- Review current files and historical runbooks for customer data, personal
  information, database identifiers, operational access details, and assets
  without established redistribution rights. Public IPs and resource IDs are
  not automatically credentials; decide deliberately what belongs in public.
- Review GitHub issues, PR discussions, Actions logs/artifacts, release bodies,
  release assets, and integration metadata that a visibility change exposes.
- Separate reusable deployment definitions and sanitized operational guidance
  from live inventories and private operational evidence. Preserve useful
  security documentation after checking disclosure risk.
- For an exposed credential, coordinate rotation and consumer verification
  before cleanup. Preserve required audit records privately before removing
  logs or historical material.
- Recommend retained history or a reviewed initial snapshot. If history or
  repository identity changes, account for signed tags, source-commit evidence,
  existing releases, Go import paths, SDK links, Homebrew, and Vercel integration.

Acceptance: a redacted disposition register covers every finding and exposed
surface. No unresolved credential or confidential-data exposure remains in
the exact publication candidate. No destructive cleanup occurs as part of
the assessment itself.

### A2. Resolve licenses and distribution obligations

- Confirm authority to license authored and contributed code and bundled assets.
- Add `LICENSE`, `THIRD_PARTY_NOTICES.md`, and `docs/licensing.md`, with explicit
  path-level exceptions for templates, examples, and retained third-party terms.
- Inventory direct and transitive Go/JavaScript dependencies, fonts, images,
  patched JuiceFS, MinIO, Redis packages, Firecracker, and guest kernel/rootfs
  contents. Distinguish source references, separate services, linked code, and
  binaries actually distributed by Cantelop.
- Document applicable notice and corresponding-source obligations for each
  distributed artifact. MinIO's AGPL license is a review item, not evidence
  that every independent platform component inherits that license.
- Include required licenses/notices and source references in CLI and host
  archives. Validate the final archives, not only files in the checkout.
- Define the version-specific source offer/link for the hosted AGPL platform
  and a process for keeping it aligned with the deployed revision.

Acceptance: the release has an approved license map, an artifact/license matrix,
and packaging checks that verify the required notices and source references.
The intended licensing of user-authored applications is clearly explained.

### A3. Make public contributions safe and useful

- Inspect actual GitHub, Vercel, deployment, and signing settings. Document which
  systems execute untrusted PR code and which credentials each can access.
- Run fork PR checks without production, deployment, registry-publishing, or
  signing credentials. Keep privileged release/deployment work on trusted refs
  with the required protections. Avoid privileged execution of unreviewed code.
- Verify Vercel fork-approval behavior and preview environment isolation. Use
  isolated disposable databases or omit database credentials for previews.
  Keep production migrations behind a trusted deployment boundary.
- Add comprehensive public CI around `make check`, with explicit SDK/toolchain
  pins and public dependency installation. Document which tests need Docker,
  KVM, root networking, or provider accounts; run those in appropriate jobs.
- Keep the existing full release qualification path. Ordinary contributor
  checks must not require privileged access to Cantelop's live deployment.
- Add `CONTRIBUTING.md`, `SECURITY.md`, contribution/sign-off guidance, and
  maintainer expectations. Explain architecture, setup, supported checks,
  installation maturity, and private vulnerability reporting from the README.
- Reconcile nearby release docs with the final workflows and licensing rules.

Acceptance: a test PR from a fresh fork completes the advertised contributor
checks without private credentials. Review evidence establishes that its build
cannot mutate the live platform or access protected release/signing material.

### A4. Prepare the exact public candidate

1. Select a reviewed commit and the exact branches/tags, history, releases, and
   GitHub surfaces that will be exposed.
2. Run the applicable repository/release checks and scan the final publication
   contents again. Resolve or accurately disclose known limits; remediate any
   identified deployed vulnerability that publication would expose first.
3. Review the README and release notes as a new reader. Label the release a
   source preview and describe the current provider and hardware requirements.
4. Prepare a publication checklist, owner, candidate identity, and reviewed
   evidence. Making the repository private later cannot retract public copies.
5. Obtain the maintainer's publication decision against that concrete candidate,
   then publish and verify anonymous source/dependency access and fork CI.

Milestone A is complete when the reviewed source is publicly available under
the chosen terms and the advertised contribution workflow works. Supported
self-hosting remains explicitly pending Milestone B.

## Milestone B: self-hosted alpha

### B1. Remove deployment identity assumptions

Provide explicit operator configuration for console and API domains, trusted
console origins, Cloudflare zones/namespaces/resources, observability endpoints,
and CLI control origins. Start with:

- `platform/cloudflare/variables.tf`, `main.tf`, and deployment/verification scripts;
- `edge/workers/*/wrangler.jsonc` and generated deployment configuration;
- `console/web/lib/console-origin.ts`, documentation origins, and OAuth setup;
- Ansible example inventories, environment examples, and CLI endpoint selection.

Preserve strict origin validation, route exclusions, service authentication,
and fail-closed behavior. Do not trust arbitrary forwarded hosts to support
custom domains. Retain existing hosted values as an explicit deployment
configuration rather than silently changing the live deployment.

Acceptance: an alternate-domain configuration builds and validates end to end;
negative tests reject untrusted origins and unsafe routing. Terraform plans for
the existing hosted configuration do not unexpectedly replace live resources.

### B2. Document and automate one reference deployment

Add `docs/self-hosting.md` as the installation entry point. Reuse existing
provisioning, artifact preparation, and qualification commands rather than
introducing a second deployment system.

- Declare the supported Linux amd64/KVM host specification and three-host
  topology. Record the exact qualified OS, toolchains, Firecracker, guest
  artifacts, and compatible SDK revision/version.
- List required accounts, permissions, provider products, domains, and cost
  categories. Start with the current Cloudflare/Turso/PostgreSQL/storage stack
  and documented console hosting; account-free operation is later work.
- Classify each credential and feature as required or optional. Explain OAuth
  callback registration, key generation, private inventory/Vault setup, secret
  storage, and which values must survive restore.
- Add a read-only preflight for missing tools, unsupported hosts, incomplete
  configuration, and provider prerequisites before creating resources.
- Document provisioning and deployment order, guest/kernel/snapshot preparation,
  database migrations, first administrator, starter deployment, backup/restore,
  upgrades, troubleshooting, and teardown/data-deletion effects.
- Ensure the build path fetches public, pinned sources without the maintainer's
  local caches, adjacent unpinned checkouts, or private registry credentials.

Acceptance: a fresh operator can follow one documented path using their own
domain and accounts. Every privilege escalation and provider-side mutation is
visible in the procedure, and failures identify the missing prerequisite.

### B3. Qualify from scratch and publish alpha support limits

Use an isolated installation, fresh credentials, and an exact candidate tag or
commit. Record evidence without secret values. Verify:

1. Installation, schema initialization, administrator setup, and both configured
   login providers.
2. CLI connection to the custom console, starter deployment, API requests,
   Session execution, logs, variables, and secrets.
3. Workspace file and database persistence across Session and host restarts.
4. Release activation and rollback, cold activation, admission failure behavior,
   and documented backup/restore for control state, storage, and encryption keys.
5. Tenant isolation checks appropriate to the installation, including the
   [documented NFS qualification](../storage/juicefs/README.md#qualification-and-rollout).
6. Resource removal and provider-cost cleanup, with explicit data-loss boundaries.

Have an operator without the author's local setup reproduce the documented
path. Convert any undocumented manual steps into instructions or automation.

Acceptance: the installation passes the stated checks on the reference topology,
with evidence tied to component revisions. Publish the supported matrix,
remaining limitations, upgrade policy, and alpha support expectations.

## Suggested delivery order and effort

Estimates are engineering effort for one person, excluding account approvals,
legal review, provider wait time, and remediation uncovered by the review.

| Work package | Deliverable | Rough effort | Dependency |
| --- | --- | --- | --- |
| A1 | Private disclosure report and proposed publication surface | 1–2 days | None |
| A2 | License map, notices, source references, artifact checks | 1–2 days | Ownership/license decisions and distribution inventory |
| A3–A4 | Safe public CI, contributor docs, reviewed source preview | 1–2 days | Disclosure and licensing clearance before publication |
| B1 | Configurable deployment identities with boundary tests | 1–3 days | Reference topology agreed |
| B2 | Reference installation docs and preflight | 2–4 days | B1 configuration contract |
| B3 | Independent installation evidence and alpha release | 2–3 days | B1 and B2 |

Target Milestone A in approximately 3–6 engineering days and Milestone B in
another 5–10. Work can overlap, but completion follows the evidence gates.
Re-estimate after A1 if history cleanup or licensing issues are substantial.

Keep changes reviewable: disclosure cleanup, licensing/packaging, contribution
CI/docs, deployment configuration, and bootstrap/qualification should be
separate changes. Live account changes and publication have their own reviewed
execution checklist; a green code change does not perform them automatically.

## Deferred work

After the alpha, prioritize from actual operator feedback: a single-host
evaluation deployment, fewer mandatory provider accounts, non-Cloudflare edge
support, alternative database/storage providers, additional architectures,
one-command installation, and broader governance. None is required to publish
the existing implementation accurately.

## References

- [Repository architecture](architecture/repository-layout.md)
- [Release candidates and qualification](release-candidates.md)
- [Deployment runbook](deployment.md)
- [Open Source Definition](https://opensource.org/osd)
- [AGPL-3.0 terms](https://www.gnu.org/licenses/agpl-3.0.html)
- [Apache-2.0 terms](https://www.apache.org/licenses/LICENSE-2.0)
- [GitHub visibility-change consequences](https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/managing-repository-settings/setting-repository-visibility)
- [Pinned MinIO license](https://github.com/minio/minio/blob/RELEASE.2025-10-15T17-29-55Z/LICENSE)
- [Pinned JuiceFS license](https://github.com/juicedata/juicefs/blob/62bedf3cdecdb763c0be11edbab21f6764159aa5/LICENSE)
