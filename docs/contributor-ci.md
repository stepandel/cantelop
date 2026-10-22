# Contributing from a fork

Fork [stepandel/cantelop](https://github.com/stepandel/cantelop), create a change
branch, and open a pull request against its `main` branch. Sign off contribution
commits as described in [CONTRIBUTING.md](../CONTRIBUTING.md).

The `Contributor checks` workflow uses public dependencies, a read-only GitHub
token, and no hosted deployment or signing credentials. Checkout credentials
are not persisted. Go, console, and edge checks run in parallel, with separate
Python/provisioning policy checks. Console tests migrate a disposable PostgreSQL
17 service; its database password is a local test fixture.

On PRs, `scripts/ci-scope.py` selects jobs from the difference between the base
commit and the tested PR merge tree. Website changes run console checks without
Go or infrastructure tests. Documentation changes run the inexpensive
architecture/API/notice checks and secret scanning. Shared contracts,
dependencies, workflows, and tooling trigger broad coverage; unknown inputs
also default to broad coverage. Main and manual runs execute all suites. The
Go job also builds the managed Linux entry points. The edge job includes the
Worker runtime integration and Cloudflare tail-settings tests.

The `secrets` job always scans reachable history and verifies that fixture
exceptions still detect new synthetic credentials in the same files. Hosted
JuiceFS qualification retains its verified-input cache and runs for changes to
its pinned build script or patch, on main/manual runs, and on contributor
workflow changes. It is restricted to the private operational repository.

Use the final `check` job as the required Actions status. It always reports and
requires every selected suite plus secret scanning to succeed, including when
another job fails or is cancelled. Unselected jobs can skip without leaving a
required workflow pending. The old `verify` (Managed Fleets), `policy`, and
`juicefs` statuses must not remain separate branch-protection requirements;
their coverage is included in the consolidated workflow. Vercel supplies the
Next.js build for hosted console changes; preview-comment metadata is not a
test or a recommended merge requirement. Repository settings are managed
separately from these files.

Ordinary checks and Vercel previews accept unpublished source changes. The
public-source snapshot is verified explicitly during release qualification,
canonical archive creation/validation, and production console builds that use a
public snapshot mapping. Private console deployments use the exact Vercel Git
revision and do not require refreshing the release snapshot after every merge.
Archive unit tests use an independent fixture source identity rather than
requiring the working tree to match a published production snapshot.

All external contributors require a maintainer's approval before fork jobs run.
Approval authorizes only these public checks. Keep unreviewed fork code outside
the private hosted repository and its privileged Vercel previews.

A successful fork smoke test establishes:

1. The pull request's head repository is a real fork of the public repository.
2. `Contributor checks` completes on the pull-request merge ref, including the
   selected suites and full-history secret scan. A console test change exercises
   the fresh database tests; a docs-only change deliberately skips them.
3. Job logs show read-only GitHub token permissions, and the public repository
   has no deployment environments or stored Actions secrets.
4. The pull request receives no hosted Vercel deployment and no privileged
   release/deployment job runs.

A test authored by an existing maintainer exercises the fork repository boundary.
It does not prove the approval experience for a first-time, unaffiliated account;
check that separately when the first outside contribution arrives.

For local checks use the pinned toolchains and installation commands in the
contribution guide. Never point `CANTELOP_TEST_DATABASE_URL` at a live database.
Real KVM, Docker, provider, and fresh-host qualification remain separate from
ordinary fork contribution checks.
