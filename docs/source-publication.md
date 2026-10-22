# Public source and private hosted deployments

Public source, issues, and contributions live at
[stepandel/cantelop](https://github.com/stepandel/cantelop). The existing hosted
repository is private as `stepandel/cantelop-internal`. Vercel remains connected
to that private repository. Hosted credentials, release/signing credentials,
historical operations evidence, and actual inventories stay private.

The public repository starts with a reviewed snapshot, without private Git
ancestry, issues, Actions logs, tags, or historical release assets. The explicit
[export policy](../.public-source-policy.json) excludes operational history and
live inventory values. Example inventories and reusable deployment source are
included. The AGPL license, MIT exceptions, dependency notices, and trademark
policy apply as described in [licensing](licensing.md).

## Contributions and deployment authority

Public pull requests run contributor checks without deployment or signing
credentials. The public repository has no Vercel project. Hosted deployment and
release workflows are retained as reference source but their jobs require the
private repository's immutable GitHub repository ID. Renaming a repository
does not change this authority boundary. Independently operated forks must
configure their own deployment workflows and credentials deliberately.

A maintainer reviews public changes, including dependency, workflow, build,
and migration changes, before promoting them into the private repository.
Never automatically execute a public branch or pull request with hosted
credentials. Public contribution acceptance and hosted deployment are separate
maintainer decisions. Preserve contribution attribution and DCO sign-offs when
cherry-picking or applying reviewed commits.

## Publish a matching source revision

From a clean private change branch, finish source changes and stage new files.
Export to a new directory outside the private checkout:

```sh
python3 scripts/public-source.py export /tmp/cantelop-public-candidate
```

The exporter reads tracked files, rejects symlinks, and writes
`SOURCE_SNAPSHOT.json` with each source file's SHA-256 and executable mode.
It resets the deployment pin in the public copy. It does not push or deploy.
Review the entire export and run the pinned secret scanner and contributor
checks. Historical fingerprint suppressions are replaced with an empty public
ignore file; the narrowly scoped fixture allowlists continue to apply.

For the initial publication, initialize a new Git repository in that directory.
For later publications, apply the exported tree to a clean clone of the public
repository, preserve its public history and contributions, and review the diff.
Never use `git push --mirror`, push private branches/tags, or merge private Git
ancestry into the public repository. Publish the reviewed snapshot commit.

Bind the exact clean public checkout back to the private change:

```sh
python3 scripts/public-source.py pin /tmp/cantelop-public-candidate --commit PUBLIC_COMMIT_SHA
python3 scripts/public-source.py verify
git add console/public-source.json SOURCE_SNAPSHOT.json
```

The pin verifies the actual public file bytes and modes against the selected
private source. It records the public commit and manifest digest. Before a
public release, require public checks to pass and the public commit to be
anonymously accessible. Publication and pinning are separate from development
PRs and private console deployments. No private deployment commit needs to
become public.

`make check` validates development changes without requiring publication.
`make check-public-source` detects changed or newly tracked source after
publication and runs explicitly at the start of `make qualify-release`.
Canonical archive preparation and validation also require the verified pin.
Production Vercel builds that use a public snapshot mapping verify the complete
on-disk source file list and contents before any database migrations, including
checkouts without `.git`. Build output, local credentials, and private
operational paths are excluded by the explicit policy. Source changes require
publishing and pinning a new snapshot before deploying with that public mapping.
Vercel previews do not claim the previous public snapshot as their own source.
Policy changes are reviewable source changes too.

With `visibility: "private"` in `console/public-source.json`, Vercel production
and preview deployments use `VERCEL_GIT_COMMIT_SHA` as their source revision and
the pin's repository URL. They do not require snapshot verification or a
pin-only follow-up commit after merging source changes. The saved private
snapshot remains available for explicit release qualification and archive
creation/validation. Keep `CANTELOP_SOURCE_COMMIT` and
`CANTELOP_SOURCE_REPOSITORY` unset on canonical hosted Vercel deployments.
Other hosts must set `CANTELOP_SOURCE_COMMIT` to their exact deployed revision;
an explicit revision that differs from Vercel's deployment commit is rejected.

The console `/source` page links the deployed private commit and labels it as
requiring operator access, or links the verified public commit when using a
public mapping. New CLI/host archives record the source commit, snapshot digest,
and separate build commit; no historical binary assets are republished by this
process. Verify anonymous source access for public releases and the deployed
`/source` link after each promotion.
Other independently deployed components still need matching source for their
actual revisions. Existing historical binaries and the hosted platform's
previous revisions are not silently reclassified as this source preview.

## Installation maturity

This release exposes the platform implementation and contributor checks.
Independently qualified installation remains Milestone B in the
[release plan](open-source-release-plan.md). Do not interpret internal rollout
procedures as a promise of a supported fresh installation.
