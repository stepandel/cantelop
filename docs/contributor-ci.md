# Contributing from a fork

Fork [stepandel/cantelop](https://github.com/stepandel/cantelop), create a change
branch, and open a pull request against its `main` branch. Sign off contribution
commits as described in [CONTRIBUTING.md](../CONTRIBUTING.md).

The `Contributor checks` workflow runs `make check` using public dependencies
and a disposable PostgreSQL 17 service. Its database password is a local test
fixture. The workflow uses a read-only GitHub token, does not persist checkout
credentials, and has no hosted deployment or signing credentials. The `secrets`
job scans reachable history and verifies that fixture exceptions still detect
new synthetic credentials in the same files. Changes to runtime/console paths
also run the `Managed Fleets` workflow.

All external contributors require a maintainer's approval before fork jobs run.
Approval authorizes only these public checks. Keep unreviewed fork code outside
the private hosted repository and its privileged Vercel previews.

A successful fork smoke test establishes:

1. The pull request's head repository is a real fork of the public repository.
2. `Contributor checks` completes on the pull-request merge ref, including the
   fresh database tests and full-history secret scan.
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
