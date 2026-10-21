# Contributing to Cantelop

Start with the [repository architecture](docs/architecture/repository-layout.md)
and [license map](docs/licensing.md). Cantelop is a public source
preview; independently qualified self-hosting is still in progress under the
[release plan](docs/open-source-release-plan.md).

## Local checks

Use the Go version in `go.mod`, Bun version in `console/package.json`, Python
3.12 or later, and Terraform 1.8 or later. Install public dependencies:

```sh
python3 -m venv .venv
. .venv/bin/activate
python3 -m pip install --requirement scripts/requirements-test.txt
(cd console && bun install --frozen-lockfile)
(cd edge/workers && bun install --frozen-lockfile)
make check
```

The ordinary checks do not require Cantelop's production credentials. Some
database tests require a disposable PostgreSQL database; CI provisions its own.
Never point tests at a live control database. Linux-only code is checked on
Linux CI. Real VM integration tests additionally require Linux, `/dev/kvm`,
trusted boot fixtures, and root networking privileges; see the README before
explicitly enabling them. Docker and provider-backed qualification have their
own documented prerequisites.

Run checks appropriate to the change. Release qualification additionally uses
the separate public SDK checkout; see [release candidates](docs/release-candidates.md).
That gate builds artifacts and does not authorize publication or deployment.

## Changes and review

Keep changes focused. Describe the user-visible behavior, relevant design
decisions, and validation. Explain any changes to persistence, API contracts,
privileged host operations, or upgrade/rollback behavior. Add meaningful tests
for behavior changes rather than tests that only repeat implementation details.

Do not include credentials, customer data, private inventories, live logs, or
confidential operational evidence. Use placeholders and disposable fixtures.
Do not put vulnerability details in a public issue; use [SECURITY.md](SECURITY.md).

Public PR jobs run without deployment or signing credentials. Maintainers must
not run unreviewed fork code in a privileged workflow or approve a Vercel build
that has access to live infrastructure credentials. Merging code does not
authorize deploying it.

Submit contributions to [stepandel/cantelop](https://github.com/stepandel/cantelop).
Vercel and hosted release automation stay connected to the private operational
repository. Reviewed changes are promoted there by a maintainer; a public PR
does not trigger a privileged preview or production deployment. See the
[publication procedure](docs/source-publication.md).

## Contribution rights and sign-off

Contributions use the license applicable to the changed files. Certify your
right to contribute by signing off each commit under the
[Developer Certificate of Origin 1.1](https://developercertificate.org/):

```sh
git commit -s
```

This adds `Signed-off-by: Your Name <your-email>` to the commit. Use an identity
and address you are comfortable publishing; the contribution record is public.
Preserve upstream copyright and license notices for copied or adapted code and
describe its source. Contact the maintainer before proposing a new dependency
with unfamiliar licensing requirements.

## Maintainer release checks

Before accepting public contributions, complete the account-setting review in
the release plan: branch protection, fork approvals, preview credential
isolation, private vulnerability reporting, and protected release/signing jobs.
Repository files alone do not enforce external GitHub or Vercel settings.
